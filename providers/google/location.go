package google

import (
	"encoding/json"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"
	"time"

	"github.com/johnnycube/archivist/internal/bundle"
	"github.com/johnnycube/archivist/internal/domain"
	"github.com/johnnycube/archivist/internal/provider"
)

// Location data comes in three generations, all mapped here:
//
//   - Semantic Location History/<year>/<year>_<MONTH>.json —
//     {"timelineObjects":[{placeVisit{…}} | {activitySegment{…}}]} with E7
//     integer coordinates and RFC3339 timestamps (older: startTimestampMs);
//   - Records.json — {"locations":[{latitudeE7, longitudeE7, timestamp |
//     timestampMs, accuracy, source, …}]}: every raw fix, millions of them;
//   - the on-device export (Timeline.json on Android, a bare JSON array from
//     iOS, "location-history.json") — semanticSegments[] with visit / activity /
//     timelinePath, coordinates as strings ("50.05°, 14.34°" or
//     "geo:50.05,14.34"), plus rawSignals[] (position, activityRecord, wifiScan)
//     and a userLocationProfile object.
//
// Google removed the server-side Timeline in 2024/2025, so new Takeouts hold
// only what the phone exports; old archives still have the first two.

// ---- Semantic Location History ----

var (
	semanticFileRe = regexp.MustCompile(`^\d{4}_\p{L}+$`)
	yearDirRe      = regexp.MustCompile(`^\d{4}$`)
)

type semanticLocationEx struct{}

// Match: <year>/<year>_<MONTH>.json — the folder names above it are localized,
// the file pattern is not.
func (semanticLocationEx) Match(member string) bool {
	return semanticFileRe.MatchString(stem(member, ".json")) && yearDirRe.MatchString(path.Base(path.Dir(member)))
}

func (semanticLocationEx) Extract(member string, r io.Reader, emit func(provider.RawRecord) error) error {
	return bundle.StreamJSONArray(r, []string{"timelineObjects"}, func(el json.RawMessage) error {
		return emit(rec("timeline_object", el))
	}, bundle.ReportSkipped(member, emit))
}

type e7Location struct {
	LatitudeE7   flex   `json:"latitudeE7"`
	LongitudeE7  flex   `json:"longitudeE7"`
	PlaceID      string `json:"placeId"`
	Address      string `json:"address"`
	Name         string `json:"name"`
	SemanticType string `json:"semanticType"`
}

type duration struct {
	StartTimestamp   flex `json:"startTimestamp"`
	EndTimestamp     flex `json:"endTimestamp"`
	StartTimestampMs flex `json:"startTimestampMs"`
	EndTimestampMs   flex `json:"endTimestampMs"`
}

func (d duration) times() (start, end time.Time, rawStart string) {
	start, rawStart = rawTime(d.StartTimestamp)
	if start.IsZero() {
		start = epoch(d.StartTimestampMs, time.Millisecond)
		rawStart = firstNonEmpty(rawStart, d.StartTimestampMs.String())
	}
	end, _ = rawTime(d.EndTimestamp)
	if end.IsZero() {
		end = epoch(d.EndTimestampMs, time.Millisecond)
	}
	return start, end, rawStart
}

type timelineObject struct {
	PlaceVisit *struct {
		Location        e7Location `json:"location"`
		Duration        duration   `json:"duration"`
		PlaceConfidence string     `json:"placeConfidence"`
		VisitConfidence flex       `json:"visitConfidence"`
	} `json:"placeVisit"`
	ActivitySegment *struct {
		StartLocation e7Location `json:"startLocation"`
		EndLocation   e7Location `json:"endLocation"`
		Duration      duration   `json:"duration"`
		Distance      flex       `json:"distance"`
		ActivityType  string     `json:"activityType"`
		Confidence    string     `json:"confidence"`
	} `json:"activitySegment"`
}

func normalizeTimelineObject(base domain.Event, raw provider.RawRecord) ([]domain.Event, error) {
	var o timelineObject
	if err := json.Unmarshal(raw.Payload, &o); err != nil {
		return nil, err
	}
	switch {
	case o.PlaceVisit != nil:
		v := o.PlaceVisit
		start, end, rawStart := v.Duration.times()
		lat, lon, ok := e7Pair(v.Location)
		ev := locationVisit(base, start, end, rawStart, lat, lon, ok, v.Location.Name, v.Location.Address, v.Location.PlaceID, v.Location.SemanticType)
		meta(ev, "confidence", v.PlaceConfidence)
		return one(ev), nil
	case o.ActivitySegment != nil:
		s := o.ActivitySegment
		start, end, rawStart := s.Duration.times()
		ev := locationTrip(base, start, end, rawStart, s.ActivityType, s.Distance)
		if lat, lon, ok := e7Pair(s.StartLocation); ok {
			metaAny(ev, "start_lat", lat)
			metaAny(ev, "start_lon", lon)
		}
		if lat, lon, ok := e7Pair(s.EndLocation); ok {
			metaAny(ev, "end_lat", lat)
			metaAny(ev, "end_lon", lon)
		}
		meta(ev, "confidence", s.Confidence)
		return one(ev), nil
	}
	return one(unknown(base, raw, "", nil)), nil
}

func e7Pair(l e7Location) (lat, lon float64, ok bool) {
	lat, ok1 := e7(l.LatitudeE7)
	lon, ok2 := e7(l.LongitudeE7)
	return lat, lon, ok1 && ok2
}

// locationVisit builds a google.location.visit (a stay at a place).
func locationVisit(base domain.Event, start, end time.Time, rawStart string, lat, lon float64, hasCoords bool, name, address, placeID, semanticType string) domain.Event {
	ev := fresh(base)
	ev.Type, ev.Category = "google.location.visit", domain.CategoryOther
	ev.OccurredAt = start
	where := firstNonEmpty(name, address, humanSemantic(semanticType))
	if where == "" && hasCoords {
		where = coord(lat) + ", " + coord(lon)
	}
	ev.Title = "Visited " + firstNonEmpty(where, "a place")
	meta(ev, "name", name)
	meta(ev, "address", address)
	meta(ev, "place_id", placeID)
	meta(ev, "semantic_type", semanticType)
	if hasCoords {
		metaAny(ev, "lat", lat)
		metaAny(ev, "lon", lon)
	}
	if !end.IsZero() {
		meta(ev, "end", end.Format(time.RFC3339))
		if !start.IsZero() {
			metaAny(ev, "duration_min", int64(end.Sub(start).Minutes()))
		}
	}
	key := placeID
	if key == "" && hasCoords {
		key = coord(lat) + "," + coord(lon)
	}
	ev.ExternalID = "g.location.visit:" + when(start, rawStart) + ":" + short(firstNonEmpty(key, name, address))
	return ev
}

// locationTrip builds a google.location.trip (movement between places).
func locationTrip(base domain.Event, start, end time.Time, rawStart, activityType string, distance flex) domain.Event {
	ev := fresh(base)
	ev.Type, ev.Category = "google.location.trip", domain.CategoryOther
	ev.OccurredAt = start
	how := humanActivity(activityType)
	ev.Title = "Traveled"
	if how != "" {
		ev.Title = "Traveled " + how
	}
	if m, ok := distance.Float(); ok && m > 0 {
		metaAny(ev, "distance_m", int64(m))
		ev.Title += fmt.Sprintf(" (%.1f km)", m/1000)
	}
	meta(ev, "activity_type", activityType)
	if !end.IsZero() {
		meta(ev, "end", end.Format(time.RFC3339))
		if !start.IsZero() {
			metaAny(ev, "duration_min", int64(end.Sub(start).Minutes()))
		}
	}
	ev.ExternalID = "g.location.trip:" + when(start, rawStart) + ":" + short(activityType+"\x00"+distance.String())
	return ev
}

// humanActivity renders IN_PASSENGER_VEHICLE as "in passenger vehicle".
func humanActivity(t string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	if t == "" || t == "unknown_activity_type" {
		return ""
	}
	return strings.ReplaceAll(t, "_", " ")
}

// humanSemantic renders TYPE_HOME as "Home".
func humanSemantic(t string) string {
	t = strings.TrimPrefix(strings.ToLower(t), "type_")
	if t == "" || t == "unknown" {
		return ""
	}
	return strings.ToUpper(t[:1]) + strings.ReplaceAll(t[1:], "_", " ")
}

// ---- Records.json ----

// recordsEx streams the raw location fixes. High volume: one event per fix.
type recordsEx struct{}

func (recordsEx) Match(member string) bool {
	return stem(member, ".json") == "records" && (dirContains(member, "location") || dirContains(member, "standort") || dirContains(member, "timeline") || dirContains(member, "zeitachse") || dirContains(member, "historique") || dirContains(member, "ubicaci") || dirContains(member, "posizione"))
}

func (recordsEx) Extract(member string, r io.Reader, emit func(provider.RawRecord) error) error {
	return bundle.StreamJSONArray(r, []string{"locations"}, func(el json.RawMessage) error {
		return emit(rec("location", el))
	}, bundle.ReportSkipped(member, emit))
}

type locationRecord struct {
	LatitudeE7  flex   `json:"latitudeE7"`
	LongitudeE7 flex   `json:"longitudeE7"`
	Timestamp   flex   `json:"timestamp"`
	TimestampMs flex   `json:"timestampMs"`
	Accuracy    flex   `json:"accuracy"`
	Altitude    flex   `json:"altitude"`
	Source      string `json:"source"`
	DeviceTag   flex   `json:"deviceTag"`
	Platform    string `json:"platformType"`
}

func normalizeLocationRecord(base domain.Event, raw provider.RawRecord) ([]domain.Event, error) {
	var l locationRecord
	if err := json.Unmarshal(raw.Payload, &l); err != nil {
		return nil, err
	}
	lat, ok1 := e7(l.LatitudeE7)
	lon, ok2 := e7(l.LongitudeE7)
	if !ok1 || !ok2 {
		return one(unknown(base, raw, "", nil)), nil
	}
	t, rawT := rawTime(l.Timestamp)
	if t.IsZero() {
		t = epoch(l.TimestampMs, time.Millisecond)
		rawT = firstNonEmpty(rawT, l.TimestampMs.String())
	}
	ev := locationPoint(base, t, rawT, lat, lon, l.Source)
	if v, ok := l.Accuracy.Float(); ok {
		metaAny(ev, "accuracy_m", v)
	}
	if v, ok := l.Altitude.Float(); ok {
		metaAny(ev, "altitude_m", v)
	}
	meta(ev, "device", l.DeviceTag.String())
	meta(ev, "platform", l.Platform)
	return one(ev), nil
}

func locationPoint(base domain.Event, t time.Time, rawT string, lat, lon float64, source string) domain.Event {
	ev := fresh(base)
	ev.Type, ev.Category = "google.location.point", domain.CategoryOther
	ev.OccurredAt = t
	ev.Title = "Location " + coord(lat) + ", " + coord(lon)
	metaAny(ev, "lat", lat)
	metaAny(ev, "lon", lon)
	meta(ev, "source", source)
	ev.ExternalID = "g.location.point:" + when(t, rawT) + ":" + coord(lat) + "," + coord(lon)
	return ev
}

// ---- on-device Timeline.json ----

type timelineEx struct{}

var timelineStems = []string{"timeline", "location-history", "zeitachse"}

func (timelineEx) Match(member string) bool {
	return contains(timelineStems, stem(member, ".json"))
}

// Extract streams semanticSegments and rawSignals; a bare array (iOS) is taken
// as segments. userLocationProfile and any other array are reported as skipped.
func (timelineEx) Extract(member string, r io.Reader, emit func(provider.RawRecord) error) error {
	return bundle.StreamJSONArraysReport(r, []string{"semanticSegments", "rawSignals"}, func(key string, el json.RawMessage) error {
		kind := "semantic_segment"
		if key == "rawSignals" {
			kind = "raw_signal"
		}
		return emit(rec(kind, el))
	}, bundle.ReportSkipped(member, emit))
}

// strLocation is a coordinate as the on-device export writes it: Android
// {"latLng": "50.05°, 14.34°"}, iOS a bare string "geo:50.05,14.34".
type strLocation string

func (s *strLocation) UnmarshalJSON(b []byte) error {
	var str string
	if err := json.Unmarshal(b, &str); err == nil {
		*s = strLocation(str)
		return nil
	}
	var obj struct {
		LatLng string `json:"latLng"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return err
	}
	*s = strLocation(obj.LatLng)
	return nil
}

type semanticSegment struct {
	StartTime flex `json:"startTime"`
	EndTime   flex `json:"endTime"`
	Visit     *struct {
		HierarchyLevel flex `json:"hierarchyLevel"`
		Probability    flex `json:"probability"`
		TopCandidate   struct {
			PlaceID       string      `json:"placeId"`
			PlaceIDiOS    string      `json:"placeID"`
			SemanticType  string      `json:"semanticType"`
			Probability   flex        `json:"probability"`
			PlaceLocation strLocation `json:"placeLocation"`
		} `json:"topCandidate"`
	} `json:"visit"`
	Activity *struct {
		Start          strLocation `json:"start"`
		End            strLocation `json:"end"`
		DistanceMeters flex        `json:"distanceMeters"`
		Probability    flex        `json:"probability"`
		TopCandidate   struct {
			Type        string `json:"type"`
			Probability flex   `json:"probability"`
		} `json:"topCandidate"`
	} `json:"activity"`
	TimelinePath []struct {
		Point                              string `json:"point"`
		Time                               flex   `json:"time"`
		DurationMinutesOffsetFromStartTime flex   `json:"durationMinutesOffsetFromStartTime"`
	} `json:"timelinePath"`
}

func normalizeSemanticSegment(base domain.Event, raw provider.RawRecord) ([]domain.Event, error) {
	var s semanticSegment
	if err := json.Unmarshal(raw.Payload, &s); err != nil {
		return nil, err
	}
	start, rawStart := rawTime(s.StartTime)
	end, _ := rawTime(s.EndTime)
	var out []domain.Event
	if v := s.Visit; v != nil {
		lat, lon, ok := parseLatLng(string(v.TopCandidate.PlaceLocation))
		placeID := firstNonEmpty(v.TopCandidate.PlaceID, v.TopCandidate.PlaceIDiOS)
		ev := locationVisit(base, start, end, rawStart, lat, lon, ok, "", "", placeID, v.TopCandidate.SemanticType)
		if p, ok := v.Probability.Float(); ok {
			metaAny(ev, "probability", p)
		}
		out = append(out, ev)
	}
	if a := s.Activity; a != nil {
		ev := locationTrip(base, start, end, rawStart, a.TopCandidate.Type, a.DistanceMeters)
		if lat, lon, ok := parseLatLng(string(a.Start)); ok {
			metaAny(ev, "start_lat", lat)
			metaAny(ev, "start_lon", lon)
		}
		if lat, lon, ok := parseLatLng(string(a.End)); ok {
			metaAny(ev, "end_lat", lat)
			metaAny(ev, "end_lon", lon)
		}
		out = append(out, ev)
	}
	for i, p := range s.TimelinePath {
		lat, lon, ok := parseLatLng(p.Point)
		if !ok {
			continue
		}
		t, rawT := rawTime(p.Time)
		if t.IsZero() && !start.IsZero() {
			if off, ok := p.DurationMinutesOffsetFromStartTime.Float(); ok { // iOS: minutes after startTime
				t = start.Add(time.Duration(off * float64(time.Minute)))
			}
		}
		if t.IsZero() {
			rawT = firstNonEmpty(rawT, rawStart) + "#" + fmt.Sprint(i)
		}
		out = append(out, locationPoint(base, t, rawT, lat, lon, "timeline_path"))
	}
	if len(out) == 0 {
		return one(unknown(base, raw, "", nil)), nil
	}
	return out, nil
}

type rawSignal struct {
	Position *struct {
		LatLng               string `json:"LatLng"`
		AccuracyMeters       flex   `json:"accuracyMeters"`
		AltitudeMeters       flex   `json:"altitudeMeters"`
		Source               string `json:"source"`
		Timestamp            flex   `json:"timestamp"`
		SpeedMetersPerSecond flex   `json:"speedMetersPerSecond"`
	} `json:"position"`
}

// normalizeRawSignal maps position fixes to google.location.point; the other
// signals (activityRecord, wifiScan) are sensor noise and become google.unknown
// records so they remain visible.
func normalizeRawSignal(base domain.Event, raw provider.RawRecord) ([]domain.Event, error) {
	var s rawSignal
	if err := json.Unmarshal(raw.Payload, &s); err != nil {
		return nil, err
	}
	if s.Position == nil {
		return one(unknown(base, raw, "", nil)), nil
	}
	lat, lon, ok := parseLatLng(s.Position.LatLng)
	if !ok {
		return one(unknown(base, raw, "", nil)), nil
	}
	t, rawT := rawTime(s.Position.Timestamp)
	ev := locationPoint(base, t, rawT, lat, lon, s.Position.Source)
	if v, ok := s.Position.AccuracyMeters.Float(); ok {
		metaAny(ev, "accuracy_m", v)
	}
	if v, ok := s.Position.AltitudeMeters.Float(); ok {
		metaAny(ev, "altitude_m", v)
	}
	if v, ok := s.Position.SpeedMetersPerSecond.Float(); ok {
		metaAny(ev, "speed_mps", v)
	}
	return one(ev), nil
}
