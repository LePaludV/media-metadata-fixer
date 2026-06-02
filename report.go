package main

import (
	"encoding/csv"
	"fmt"
	"os"
	"sync"
)

// Status enumerates the possible outcomes for a single media file.
type Status string

const (
	StatusOK      Status = "ok"
	StatusSkipped Status = "skipped"
	StatusNoMatch Status = "no_match"
	StatusError   Status = "error"
	StatusDryRun  Status = "dry_run"
)

// ReportRow is one line in the output CSV.
type ReportRow struct {
	SourcePath string
	OutputPath string
	Status     Status
	JSONPath   string
	DateTaken  string // "" if unknown
	GPS        string // "lat,lon" or ""
	Error      string
}

// Reporter writes ReportRows to a CSV file. Methods are safe for concurrent
// use by multiple workers.
type Reporter struct {
	mu     sync.Mutex
	w      *csv.Writer
	file   *os.File
	counts map[Status]int
}

// NewReporter creates a CSV at path and writes the header row.
func NewReporter(path string) (*Reporter, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	r := &Reporter{
		w:      csv.NewWriter(f),
		file:   f,
		counts: map[Status]int{},
	}
	if err := r.w.Write([]string{
		"source_path", "output_path", "status", "json_path", "date_taken", "gps", "error",
	}); err != nil {
		f.Close()
		return nil, err
	}
	return r, nil
}

// Write appends a row to the CSV and bumps the per-status counter.
func (r *Reporter) Write(row ReportRow) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counts[row.Status]++
	_ = r.w.Write([]string{
		row.SourcePath,
		row.OutputPath,
		string(row.Status),
		row.JSONPath,
		row.DateTaken,
		row.GPS,
		row.Error,
	})
}

// Close flushes and closes the underlying file.
func (r *Reporter) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.w.Flush()
	if err := r.w.Error(); err != nil {
		r.file.Close()
		return err
	}
	return r.file.Close()
}

// Summary returns a formatted human-readable counts string.
func (r *Reporter) Summary() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return fmt.Sprintf("ok=%d skipped=%d no_match=%d error=%d dry_run=%d",
		r.counts[StatusOK], r.counts[StatusSkipped],
		r.counts[StatusNoMatch], r.counts[StatusError],
		r.counts[StatusDryRun])
}
