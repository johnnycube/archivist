package google

import (
	"encoding/json"
	"io"
	"path"
	"strings"
	"time"

	"github.com/johnnycube/archivist/internal/bundle"
	"github.com/johnnycube/archivist/internal/domain"
	"github.com/johnnycube/archivist/internal/provider"
)

// Google Photos exports the media files plus one JSON sidecar per item
// ("Google Photos/Photos from 2024/IMG_0001.jpg.json", since late 2024
// "IMG_0001.jpg.supplemental-metadata.json", truncated to fit the 51-character
// file name limit: "….supplemental-metad.json"): {title, description,
// imageViews, creationTime{timestamp, formatted}, photoTakenTime{…},
// geoData{latitude, longitude, altitude}, geoDataExif{…}, url, people[{name}],
// favorited, googlePhotosOrigin{…}}. The album's own metadata.json (no inner
// extension in its name) stays generic.
//
// The media file is stored as a blob by the importer (media by extension), and
// the event points at it by the sidecar's title in the same folder. When Takeout
// renamed the file ("IMG_0001(1).jpg") that reference does not resolve; the
// sidecar's data is still the event.

type photoMetaEx struct{}

func (photoMetaEx) Match(member string) bool {
	s := stem(member, ".json")
	return s != "" && strings.Contains(s, ".") && (dirContains(member, "photos") || dirContains(member, "foto"))
}

func (photoMetaEx) Extract(_ string, r io.Reader, emit func(provider.RawRecord) error) error {
	var doc json.RawMessage
	if err := bundle.ReadJSON(r, &doc); err != nil {
		return err
	}
	return emit(rec("photo_meta", doc))
}

type photoTime struct {
	Timestamp flex   `json:"timestamp"`
	Formatted string `json:"formatted"`
}

type geoData struct {
	Latitude  flex `json:"latitude"`
	Longitude flex `json:"longitude"`
	Altitude  flex `json:"altitude"`
}

type photoMeta struct {
	Title          string    `json:"title"`
	Description    string    `json:"description"`
	ImageViews     flex      `json:"imageViews"`
	CreationTime   photoTime `json:"creationTime"`
	PhotoTakenTime photoTime `json:"photoTakenTime"`
	GeoData        geoData   `json:"geoData"`
	GeoDataExif    geoData   `json:"geoDataExif"`
	URL            string    `json:"url"`
	People         []struct {
		Name string `json:"name"`
	} `json:"people"`
	Favorited bool `json:"favorited"`
	Origin    map[string]struct {
		DeviceType string `json:"deviceType"`
	} `json:"googlePhotosOrigin"`
}

func normalizePhotoMeta(base domain.Event, raw provider.RawRecord) ([]domain.Event, error) {
	var p photoMeta
	if err := json.Unmarshal(raw.Payload, &p); err != nil {
		return nil, err
	}
	if p.Title == "" && p.URL == "" && p.PhotoTakenTime.Timestamp == "" {
		return one(unknown(base, raw, "", nil)), nil
	}
	ev := base
	ev.Type, ev.Category = "google.photos.photo", domain.CategoryMedia
	ev.OccurredAt = epoch(p.PhotoTakenTime.Timestamp, time.Second)
	if ev.OccurredAt.IsZero() {
		ev.OccurredAt = epoch(p.CreationTime.Timestamp, time.Second)
	}
	ev.Title = firstNonEmpty(p.Title, "Photo")
	ev.Body = p.Description
	dir := path.Dir(raw.Member)
	if p.Title != "" {
		ev.Media = []domain.Media{{BlobKey: dir + "/" + p.Title, URL: p.URL}}
	}
	meta(ev, "url", p.URL)
	meta(ev, "album", path.Base(dir))
	if t := epoch(p.CreationTime.Timestamp, time.Second); !t.IsZero() {
		meta(ev, "uploaded", t.Format(time.RFC3339))
	}
	geo := p.GeoData
	if lat, _ := geo.Latitude.Float(); lat == 0 {
		geo = p.GeoDataExif
	}
	if lat, ok := geo.Latitude.Float(); ok && lat != 0 {
		if lon, ok := geo.Longitude.Float(); ok {
			metaAny(ev, "lat", lat)
			metaAny(ev, "lon", lon)
		}
	}
	if v, ok := p.ImageViews.Int(); ok && v > 0 {
		metaAny(ev, "views", v)
	}
	var people []string
	for _, person := range p.People {
		if person.Name != "" {
			people = append(people, person.Name)
		}
	}
	meta(ev, "people", strings.Join(people, ", "))
	if p.Favorited {
		ev.Metadata["favorited"] = true
	}
	for k, o := range p.Origin {
		meta(ev, "origin", k)
		meta(ev, "device", o.DeviceType)
	}
	// The photos.google.com URL is the item's identity across exports; the
	// same file re-exported in another album keeps it.
	key := p.URL
	if key == "" {
		key = dir + "/" + p.Title + "\x00" + p.PhotoTakenTime.Timestamp.String()
	}
	ev.ExternalID = "g.photos.photo:" + short(key)
	return one(ev), nil
}
