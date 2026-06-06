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

// editedSuffixRe matches Google's "edited copy" suffixes that all point back
// to the original media's sidecar:
//   - ~N numeric suffix:       IMG_20241002_175132~2.jpg
//   - localized "edited" word: Screenshot_20170703-000431-modifié.jpg (FR),
//                              IMG_xxx-edited.jpg (EN)
// In every case the original base name (IMG_20241002_175132.jpg, …) is what
// the JSON's title field references. Case-insensitive so "-Modifié" matches.
// The "é" is accepted both precomposed (NFC, U+00E9) and decomposed
// (NFD, "e" + U+0301) — macOS stores filenames decomposed.
var editedSuffixRe = regexp.MustCompile(`(?i)(?:~\d+|-modifi(?:é|e\x{0301})e?|-edited)(\.[^.]+)$`)

// stripEditedSuffix removes the edit suffix just before the file extension,
// returning the original base name. If no suffix is found, the input is
// returned unchanged.
func stripEditedSuffix(name string) string {
	return editedSuffixRe.ReplaceAllString(name, "$1")
}

// stemKey returns the lower-cased filename without its extension. It's used to
// pair a Motion Photo / Live Photo video (e.g. MVIMG_x.MP4) with the sidecar
// of its still counterpart (MVIMG_x.jpg) when the video has no JSON of its own.
func stemKey(name string) string {
	return strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name)))
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
//  2. Same directory + name with edit suffix (~N, -modifié, -edited) stripped
//  3. Global index by name alone (any directory) — handles Google Takeout
//     splits across multiple archives and duplicated album folders
//  4. Global index by stripped name
//  5. For videos only: the sidecar of the still image sharing the same stem
//     (Motion Photo / Live Photo: MVIMG_x.MP4 borrows MVIMG_x.jpg's JSON)
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
	// Stem index: filename without extension -> sidecar, built only from
	// still-image titles. Lets an unmatched video reuse the still's metadata
	// (Motion / Live Photo). On collision the first one wins.
	stemIndex := make(map[string]indexEntry, len(walked.JSONFiles))
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
		if !isVideo(side.Title) {
			if sk := stemKey(side.Title); sk != "" {
				if _, exists := stemIndex[sk]; !exists {
					stemIndex[sk] = e
				}
			}
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
		// Motion Photo / Live Photo: a video with no sidecar of its own
		// borrows the still image's JSON (same capture time and GPS).
		if !ok && isVideo(base) {
			e, ok = stemIndex[stemKey(stripped)]
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
