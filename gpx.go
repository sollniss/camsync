package camsync

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"time"
)

// ReadGPX reads a GPS track file and returns one path per track segment,
// ready for Track.Add, with the points named after the file. A recorded
// track places photos far more densely than located photos can.
func ReadGPX(path string) ([][]PositionObservation, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	paths, err := ParseGPX(f, path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return paths, nil
}

// ParseGPX parses GPX 1.0 or 1.1 data and returns one path per track
// segment; source names the origin of the points (see Fix.From). Points
// without a time are skipped, since they cannot place anything. GPX times
// are UTC.
func ParseGPX(r io.Reader, source string) ([][]PositionObservation, error) {
	dec := xml.NewDecoder(r)
	var paths [][]PositionObservation
	var seg []PositionObservation
	root := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.EndElement:
			if t.Name.Local == "trkseg" && len(seg) > 0 {
				paths, seg = append(paths, seg), nil
			}
		case xml.StartElement:
			switch t.Name.Local {
			case "gpx":
				root = true
			case "trkpt":
				var pt struct {
					Lat  float64 `xml:"lat,attr"`
					Lon  float64 `xml:"lon,attr"`
					Time string  `xml:"time"`
				}
				if err := dec.DecodeElement(&pt, &t); err != nil {
					return nil, err
				}
				if strings.TrimSpace(pt.Time) == "" {
					continue
				}
				at, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(pt.Time))
				if err != nil {
					return nil, fmt.Errorf("track point time %q: %w", pt.Time, err)
				}
				if math.Abs(pt.Lat) > 90 || math.Abs(pt.Lon) > 180 {
					return nil, fmt.Errorf("track point out of range: %v, %v", pt.Lat, pt.Lon)
				}
				seg = append(seg, PositionObservation{Instant: at.UTC(), Position: Position{pt.Lat, pt.Lon}, Source: source})
			}
		}
	}
	if !root {
		return nil, errors.New("not a GPX file")
	}
	if len(seg) > 0 {
		paths = append(paths, seg)
	}
	return paths, nil
}
