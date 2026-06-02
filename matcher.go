package main

import (
	"path/filepath"
	"regexp"
	"strings"
)

// Pair links a media file to its sidecar JSON (if found).
type Pair struct {
	MediaPath string       // absolute path of the media file
	JSONPath  string       // absolute path of the matched JSON, empty if no match
	Sidecar   *SidecarJSON // parsed sidecar content, nil if no match
}

// editedSuffixRe matches Google's "edited copy" suffix: ~2, ~3, …
// e.g. IMG_20241002_175132~2.jpg -> base IMG_20241002_175132.jpg
var editedSuffixRe = regexp.MustCompile(`~\d+(\.[^.]+)$`)

// stripEditedSuffix removes ~N just before the file extension, returning the
// original base name. If no suffix is found, the input is returned unchanged.
func stripEditedSuffix(name string) string {
	return editedSuffixRe.ReplaceAllString(name, "$1")
}

// indexKey builds the map key used to pair a JSON with a media file:
// the JSON's directory + the (lower-cased) media filename it references.
func indexKey(dir, title string) string {
	return dir + "\x00" + strings.ToLower(title)
}

// indexEntry holds a parsed sidecar along with its source file path.
type indexEntry struct {
	path string
	side *SidecarJSON
}

// Match consumes the walker output, parses every JSON to extract the media
// title it references, then pairs each media file with its sidecar. Pairs
// without a sidecar still get emitted (JSONPath empty) so callers can decide
// how to handle them (copy as-is, log, …).
//
// Matching order per media file:
//  1. Same directory + exact name
//  2. Same directory + name with ~N edit suffix stripped
//  3. Global index by name alone (any directory) — handles Google Takeout
//     splits across multiple archives and duplicated album folders
//  4. Global index by stripped name
//
// Orphan JSONs (parsed sidecars that didn't match any media) are returned
// separately so the reporter can log them.
func Match(walked *WalkResult) (pairs []Pair, orphanJSONs []string, parseErrors map[string]error) {
	parseErrors = map[string]error{}
	// Primary index: directory + title — strongest signal, no ambiguity.
	dirIndex := make(map[string]indexEntry, len(walked.JSONFiles))
	// Fallback index: title alone — used when media and JSON live in
	// different folders (album dupes, split archives). On collision we
	// keep the first one; duplicates of the same photo carry the same
	// metadata anyway.
	nameIndex := make(map[string]indexEntry, len(walked.JSONFiles))
	consumed := make(map[string]bool, len(walked.JSONFiles))

	for _, jsonPath := range walked.JSONFiles {
		side, err := LoadSidecar(jsonPath)
		if err != nil {
			parseErrors[jsonPath] = err
			continue
		}
		if side.Title == "" || !IsMedia(side.Title) {
			continue
		}
		e := indexEntry{path: jsonPath, side: side}
		dirIndex[indexKey(filepath.Dir(jsonPath), side.Title)] = e
		nameKey := strings.ToLower(side.Title)
		if _, exists := nameIndex[nameKey]; !exists {
			nameIndex[nameKey] = e
		}
	}

	for _, mediaPath := range walked.MediaFiles {
		dir := filepath.Dir(mediaPath)
		base := filepath.Base(mediaPath)
		stripped := stripEditedSuffix(base)
		p := Pair{MediaPath: mediaPath}

		var (
			e  indexEntry
			ok bool
		)
		if e, ok = dirIndex[indexKey(dir, base)]; !ok && stripped != base {
			e, ok = dirIndex[indexKey(dir, stripped)]
		}
		if !ok {
			e, ok = nameIndex[strings.ToLower(base)]
		}
		if !ok && stripped != base {
			e, ok = nameIndex[strings.ToLower(stripped)]
		}
		if ok {
			p.JSONPath = e.path
			p.Sidecar = e.side
			consumed[e.path] = true
		}
		pairs = append(pairs, p)
	}

	// Iterate the dir index for orphan reporting — it has one entry per
	// JSON file, while nameIndex collapses duplicates.
	for _, e := range dirIndex {
		if !consumed[e.path] {
			orphanJSONs = append(orphanJSONs, e.path)
		}
	}
	return pairs, orphanJSONs, parseErrors
}
