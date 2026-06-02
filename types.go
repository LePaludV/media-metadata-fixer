package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"
)

type timestampField struct {
	Timestamp string `json:"timestamp"`
	Formatted string `json:"formatted"`
}

type geoData struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Altitude  float64 `json:"altitude"`
}

// SidecarJSON mirrors the Google Takeout .supplemental-metadata.json files.
// We only parse the fields we actually use.
type SidecarJSON struct {
	Title          string         `json:"title"`
	Description    string         `json:"description"`
	CreationTime   timestampField `json:"creationTime"`
	PhotoTakenTime timestampField `json:"photoTakenTime"`
	GeoData        geoData        `json:"geoData"`
	GeoDataExif    geoData        `json:"geoDataExif"`
}

// PhotoTaken returns the photo-taken time as a UTC time.Time, or an error
// if the timestamp is missing or invalid.
func (s *SidecarJSON) PhotoTaken() (time.Time, error) {
	if s.PhotoTakenTime.Timestamp == "" {
		return time.Time{}, fmt.Errorf("photoTakenTime is empty")
	}
	ts, err := strconv.ParseInt(s.PhotoTakenTime.Timestamp, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid timestamp %q: %w", s.PhotoTakenTime.Timestamp, err)
	}
	return time.Unix(ts, 0).UTC(), nil
}

// BestGeo returns the geo data to use. geoDataExif takes priority (it's the
// real EXIF data) and falls back to geoData (Google-inferred). Returns ok=false
// if both are zero/empty.
func (s *SidecarJSON) BestGeo() (lat, lon, alt float64, ok bool) {
	if s.GeoDataExif.Latitude != 0 || s.GeoDataExif.Longitude != 0 {
		return s.GeoDataExif.Latitude, s.GeoDataExif.Longitude, s.GeoDataExif.Altitude, true
	}
	if s.GeoData.Latitude != 0 || s.GeoData.Longitude != 0 {
		return s.GeoData.Latitude, s.GeoData.Longitude, s.GeoData.Altitude, true
	}
	return 0, 0, 0, false
}

// LoadSidecar reads and parses a sidecar JSON file from disk.
func LoadSidecar(path string) (*SidecarJSON, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var s SidecarJSON
	if err := json.NewDecoder(f).Decode(&s); err != nil {
		return nil, fmt.Errorf("decoding %s: %w", path, err)
	}
	return &s, nil
}
