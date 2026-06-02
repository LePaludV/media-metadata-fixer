package main

import (
	"io/fs"
	"path/filepath"
	"strings"
)

// mediaExtensions is the set of file extensions we recognize as media we
// want to process. Lower-cased, with leading dot.
var mediaExtensions = map[string]bool{
	".jpg":  true,
	".jpeg": true,
	".png":  true,
	".heic": true,
	".heif": true,
	".gif":  true,
	".webp": true,
	".mp4":  true,
	".mov":  true,
	".m4v":  true,
	".3gp":  true,
	".avi":  true,
}

// IsMedia reports whether the path looks like a media file we can process.
func IsMedia(path string) bool {
	return mediaExtensions[strings.ToLower(filepath.Ext(path))]
}

// IsJSON reports whether the path is a JSON file (sidecar or album metadata).
func IsJSON(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".json")
}

// WalkResult holds the raw output of a source-tree scan.
type WalkResult struct {
	MediaFiles []string // absolute paths to media files
	JSONFiles  []string // absolute paths to JSON files (any kind)
	Skipped    []string // files ignored (unknown extensions)
}

// Walk scans the source directory and classifies every file.
// It does not parse JSONs or pair them with media — that's the matcher's job.
func Walk(root string) (*WalkResult, error) {
	res := &WalkResult{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		switch {
		case IsJSON(path):
			res.JSONFiles = append(res.JSONFiles, path)
		case IsMedia(path):
			res.MediaFiles = append(res.MediaFiles, path)
		default:
			res.Skipped = append(res.Skipped, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return res, nil
}
