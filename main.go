package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"runtime"
	"syscall"
)

func main() {
	var (
		source  = flag.String("source", "", "path to Takeout root (required)")
		output  = flag.String("output", "", "path to output root (required)")
		report  = flag.String("report", "report.csv", "path to CSV report")
		workers = flag.Int("workers", 0, "number of parallel workers (0 = NumCPU)")
		dryRun  = flag.Bool("dry-run", false, "scan and log only, write nothing")
		verbose = flag.Bool("verbose", false, "log every file processed")
	)
	flag.Parse()

	if *source == "" || *output == "" {
		fmt.Fprintln(os.Stderr, "usage: media-metadata-fixer --source <dir> --output <dir> [--dry-run] [--verbose] [--workers N] [--report path.csv]")
		os.Exit(2)
	}

	if *workers <= 0 {
		*workers = runtime.NumCPU()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Ctrl-C cleanly cancels in-flight exiftool calls.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigs
		log.Println("interrupt received, stopping…")
		cancel()
	}()

	if !*dryRun {
		if err := CheckExiftool(ctx); err != nil {
			log.Fatalln(err)
		}
		if err := os.MkdirAll(*output, 0o755); err != nil {
			log.Fatalf("cannot create output dir: %v", err)
		}
	}

	log.Printf("scanning %s…", *source)
	walked, err := Walk(*source)
	if err != nil {
		log.Fatalf("walk failed: %v", err)
	}
	log.Printf("found %d media files, %d JSON files, %d skipped (unknown extensions)",
		len(walked.MediaFiles), len(walked.JSONFiles), len(walked.Skipped))

	pairs, orphanJSONs, parseErrors := Match(walked)
	log.Printf("matched %d media files; %d orphan JSONs; %d JSON parse errors",
		countMatched(pairs), len(orphanJSONs), len(parseErrors))
	for path, err := range parseErrors {
		log.Printf("json parse error: %s: %v", path, err)
	}

	rep, err := NewReporter(*report)
	if err != nil {
		log.Fatalf("cannot open report: %v", err)
	}
	defer rep.Close()
	for _, orphan := range orphanJSONs {
		rep.Write(ReportRow{
			SourcePath: orphan,
			Status:     StatusNoMatch,
			Error:      "orphan JSON (no matching media file)",
		})
	}

	proc := &Processor{
		Opts: Options{
			OutputRoot: *output,
			DryRun:     *dryRun,
			Verbose:    *verbose,
			Workers:    *workers,
		},
		Reporter: rep,
	}
	log.Printf("processing with %d workers (dry-run=%v)", *workers, *dryRun)
	proc.Run(ctx, pairs)

	log.Printf("done. %s", rep.Summary())
	log.Printf("report written to %s", *report)
}

func countMatched(pairs []Pair) int {
	n := 0
	for _, p := range pairs {
		if p.Sidecar != nil {
			n++
		}
	}
	return n
}
