package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/schollz/progressbar/v3"
)

// Options bundles every CLI-driven setting the processor needs.
type Options struct {
	OutputRoot string
	DryRun     bool
	Verbose    bool
	Workers    int
}

// Processor runs the worker pool. Build it once, call Run, then read the
// reporter's Summary.
type Processor struct {
	Opts     Options
	Reporter *Reporter
	bar      *progressbar.ProgressBar
}

// Run dispatches all pairs across the worker pool and blocks until done.
// Context cancellation propagates to running exiftool invocations.
func (p *Processor) Run(ctx context.Context, pairs []Pair) {
	p.bar = progressbar.NewOptions(len(pairs),
		progressbar.OptionSetDescription("Processing"),
		progressbar.OptionShowCount(),
		progressbar.OptionSetPredictTime(true),
		progressbar.OptionShowIts(),
		progressbar.OptionThrottle(200_000_000), // 200ms
	)

	jobs := make(chan Pair, p.Opts.Workers*2)
	var wg sync.WaitGroup
	for i := 0; i < p.Opts.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for pair := range jobs {
				p.handle(ctx, pair)
				_ = p.bar.Add(1)
			}
		}()
	}

	for _, pair := range pairs {
		select {
		case <-ctx.Done():
			break
		case jobs <- pair:
		}
	}
	close(jobs)
	wg.Wait()
	_ = p.bar.Finish()
	fmt.Println()
}

// handle processes one pair end-to-end: route, copy, exiftool, report.
func (p *Processor) handle(ctx context.Context, pair Pair) {
	row := ReportRow{SourcePath: pair.MediaPath, JSONPath: pair.JSONPath}

	if pair.Sidecar == nil {
		// No JSON: copy as-is into _unknown/ so the user keeps the file.
		dest := filepath.Join(p.Opts.OutputRoot, "_unknown", filepath.Base(pair.MediaPath))
		row.OutputPath = dest
		row.Status = StatusNoMatch
		if !p.Opts.DryRun {
			if err := copyWithDedup(pair.MediaPath, &dest); err != nil {
				row.Status = StatusError
				row.Error = err.Error()
			} else {
				row.OutputPath = dest
			}
		}
		if p.Opts.Verbose {
			log.Printf("no_match: %s -> %s", pair.MediaPath, dest)
		}
		p.Reporter.Write(row)
		return
	}

	taken, err := pair.Sidecar.PhotoTaken()
	if err != nil {
		row.Status = StatusError
		row.Error = "photoTakenTime: " + err.Error()
		p.Reporter.Write(row)
		return
	}
	row.DateTaken = taken.Format("2006-01-02 15:04:05")
	if lat, lon, _, ok := pair.Sidecar.BestGeo(); ok {
		row.GPS = fmt.Sprintf("%f,%f", lat, lon)
	}

	destDir := filepath.Join(p.Opts.OutputRoot,
		fmt.Sprintf("%04d", taken.Year()),
		fmt.Sprintf("%02d", taken.Month()),
	)
	dest := filepath.Join(destDir, filepath.Base(pair.MediaPath))
	row.OutputPath = dest

	if p.Opts.DryRun {
		row.Status = StatusDryRun
		if p.Opts.Verbose {
			log.Printf("dry_run: %s -> %s (date=%s gps=%s)",
				pair.MediaPath, dest, row.DateTaken, row.GPS)
		}
		p.Reporter.Write(row)
		return
	}

	// Idempotence: if destination already exists with the same size, assume
	// a previous run handled it and skip both copy & exiftool.
	if same, err := sameSize(pair.MediaPath, dest); err == nil && same {
		row.Status = StatusSkipped
		if p.Opts.Verbose {
			log.Printf("skipped (already exists): %s", dest)
		}
		p.Reporter.Write(row)
		return
	}

	if err := copyWithDedup(pair.MediaPath, &dest); err != nil {
		row.Status = StatusError
		row.Error = "copy: " + err.Error()
		p.Reporter.Write(row)
		return
	}
	row.OutputPath = dest

	args, _ := BuildExiftoolArgs(dest, pair.Sidecar)
	cctx, cancel := shortTimeout(ctx)
	if err := RunExiftool(cctx, args); err != nil {
		cancel()
		row.Status = StatusError
		row.Error = err.Error()
		p.Reporter.Write(row)
		return
	}
	cancel()

	row.Status = StatusOK
	if p.Opts.Verbose {
		log.Printf("ok: %s -> %s", pair.MediaPath, dest)
	}
	p.Reporter.Write(row)
}

// sameSize reports whether src and dest exist with identical byte size.
// Used as a cheap idempotence check.
func sameSize(src, dest string) (bool, error) {
	si, err := os.Stat(src)
	if err != nil {
		return false, err
	}
	di, err := os.Stat(dest)
	if err != nil {
		return false, err
	}
	return si.Size() == di.Size(), nil
}

// copyWithDedup copies src to *dest. If *dest exists with a different size,
// it appends " (1)", " (2)", … to the basename until a free path is found
// and updates *dest to point at the new location.
func copyWithDedup(src string, dest *string) error {
	if err := os.MkdirAll(filepath.Dir(*dest), 0o755); err != nil {
		return err
	}
	final := *dest
	for i := 1; ; i++ {
		if _, err := os.Stat(final); os.IsNotExist(err) {
			break
		}
		ext := filepath.Ext(*dest)
		base := strings.TrimSuffix(filepath.Base(*dest), ext)
		final = filepath.Join(filepath.Dir(*dest), fmt.Sprintf("%s (%d)%s", base, i, ext))
	}
	*dest = final
	return copyFile(src, final)
}

// copyFile streams src -> dest. Uses a buffered io.Copy so even multi-GB
// videos don't blow up memory.
func copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(dest)
		return err
	}
	return out.Close()
}
