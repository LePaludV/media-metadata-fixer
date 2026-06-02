package main

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// exiftoolDateFormat is the date layout exiftool expects ("YYYY:MM:DD HH:MM:SS").
const exiftoolDateFormat = "2006:01:02 15:04:05"

// videoExtensions enumerates the formats that need QuickTime-style tags
// instead of (or in addition to) the standard EXIF date tags.
var videoExtensions = map[string]bool{
	".mp4": true,
	".mov": true,
	".m4v": true,
	".3gp": true,
}

func isVideo(path string) bool {
	return videoExtensions[strings.ToLower(filepath.Ext(path))]
}

// BuildExiftoolArgs returns the exiftool argv (without the leading "exiftool")
// to inject the sidecar's date and GPS into the target file. The target file
// must already exist on disk; exiftool modifies it in place.
//
// -overwrite_original avoids the _original backup file (we already kept the
// source intact in the input directory).
func BuildExiftoolArgs(target string, side *SidecarJSON) ([]string, error) {
	args := []string{"-overwrite_original"}

	taken, err := side.PhotoTaken()
	if err == nil {
		dateStr := taken.Format(exiftoolDateFormat)
		// AllDates sets DateTimeOriginal, CreateDate, ModifyDate for images.
		args = append(args, "-AllDates="+dateStr)
		// Filesystem timestamps — what Finder shows in the columns
		// "Date de modification" and "Date de création".
		// FileCreateDate is only supported on darwin and windows.
		args = append(args, "-FileModifyDate="+dateStr)
		if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
			args = append(args, "-FileCreateDate="+dateStr)
		}
		if isVideo(target) {
			// Videos store dates under QuickTime; tell exiftool to interpret
			// the value as UTC so players show the right local time.
			args = append(args, "-api", "QuickTimeUTC=1")
			args = append(args,
				"-QuickTime:CreateDate="+dateStr,
				"-QuickTime:ModifyDate="+dateStr,
				"-QuickTime:TrackCreateDate="+dateStr,
				"-QuickTime:TrackModifyDate="+dateStr,
				"-QuickTime:MediaCreateDate="+dateStr,
				"-QuickTime:MediaModifyDate="+dateStr,
			)
		}
	}

	if lat, lon, alt, ok := side.BestGeo(); ok {
		latRef, lonRef := "N", "E"
		if lat < 0 {
			latRef = "S"
			lat = -lat
		}
		if lon < 0 {
			lonRef = "W"
			lon = -lon
		}
		altRef := "0" // above sea level
		if alt < 0 {
			altRef = "1"
			alt = -alt
		}
		args = append(args,
			fmt.Sprintf("-GPSLatitude=%f", lat),
			"-GPSLatitudeRef="+latRef,
			fmt.Sprintf("-GPSLongitude=%f", lon),
			"-GPSLongitudeRef="+lonRef,
			fmt.Sprintf("-GPSAltitude=%f", alt),
			"-GPSAltitudeRef="+altRef,
		)
	}

	args = append(args, target)
	return args, nil
}

// RunExiftool executes exiftool with the given args. It returns the combined
// stdout/stderr on error so callers can log what went wrong.
func RunExiftool(ctx context.Context, args []string) error {
	cmd := exec.CommandContext(ctx, "exiftool", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("exiftool failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// CheckExiftool verifies that exiftool is available on PATH. Call once at
// startup so we fail fast instead of erroring per-file.
func CheckExiftool(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "exiftool", "-ver")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("exiftool not found on PATH (install with `brew install exiftool` on macOS): %w", err)
	}
	return nil
}

// shortTimeout returns a context with a 2-minute timeout for a single
// exiftool invocation. Avoids hung subprocesses.
func shortTimeout(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, 2*time.Minute)
}
