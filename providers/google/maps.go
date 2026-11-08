package google

import (
	"encoding/json"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/johnnycube/archivist/internal/bundle"
	"github.com/johnnycube/archivist/internal/domain"
	"github.com/johnnycube/archivist/internal/provider"
)

// Maps exports three GeoJSON FeatureCollections under "Maps (your places)/" and
// "Maps/": Saved Places.json (starred and saved places: properties.Title,
// "Google Maps URL", Location{Address, "Business Name", "Country Code",
// "Geo Coordinates"{Latitude, Longitude}}, Published, Updated), Reviews.json
// (the same plus "Star Rating" / "Five Star Rating Published" and the review
// text) and "My labeled places/Labeled places.json" (Home, Work, …). Saved
// lists ("Saved/Want to go.csv": Title, Note, URL, Comment) have no
// coordinates and no dates. Features are told apart by their properties, not
// by file name, since the names are localized.

// ---- GeoJSON features ----

type mapsFeaturesEx struct{}

var mapsFeatureStems = []string{
	"saved places", "gespeicherte orte", "lieux enregistrés", "lugares guardados", "luoghi salvati",
	"reviews", "rezensionen", "avis", "reseñas", "recensioni",
	"labeled places", "labelled places", "markierte orte", "lieux libellés", "lugares etiquetados", "luoghi etichettati",
}

func (mapsFeaturesEx) Match(member string) bool {
	return dirContains(member, "maps") && stemHasPrefix(member, ".json", mapsFeatureStems)
}

func (mapsFeaturesEx) Extract(member string, r io.Reader, emit func(provider.RawRecord) error) error {
	return bundle.StreamJSONArray(r, []string{"features"}, func(el json.RawMessage) error {
		return emit(rec("maps_feature", el))
	}, bundle.ReportSkipped(member, emit))
}

type mapsFeature struct {
	Geometry struct {
		Type        string `json:"type"`
		Coordinates []flex `json:"coordinates"` // [lon, lat]
	} `json:"geometry"`
	Properties map[string]json.RawMessage `json:"properties"`
}

// prop returns, for the first wanted word that has one, the scalar property
// whose lower-cased key equals it, else the first (in sorted order) whose key
// contains it. Numbers are rendered; objects and arrays are skipped.
func prop(props map[string]json.RawMessage, wants ...string) string {
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	scalar := func(k string) string {
		var f flex
		if err := json.Unmarshal(props[k], &f); err != nil {
			return ""
		}
		s := strings.TrimSpace(f.String())
		if s == "" || s == "null" || strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[") {
			return ""
		}
		return s
	}
	for _, want := range wants {
		for _, k := range keys {
			if strings.ToLower(k) == want {
				if s := scalar(k); s != "" {
					return s
				}
			}
		}
		for _, k := range keys {
			if strings.Contains(strings.ToLower(k), want) {
				if s := scalar(k); s != "" {
					return s
				}
			}
		}
	}
	return ""
}

func normalizeMapsFeature(base domain.Event, raw provider.RawRecord) ([]domain.Event, error) {
	var f mapsFeature
	if err := json.Unmarshal(raw.Payload, &f); err != nil {
		return nil, err
	}
	var loc struct {
		Address      string `json:"Address"`
		BusinessName string `json:"Business Name"`
		CountryCode  string `json:"Country Code"`
		Geo          struct {
			Latitude  flex `json:"Latitude"`
			Longitude flex `json:"Longitude"`
		} `json:"Geo Coordinates"`
	}
	if l, ok := f.Properties["Location"]; ok {
		_ = json.Unmarshal(l, &loc) // best effort; a string here is fine too
	}
	title := firstNonEmpty(prop(f.Properties, "title", "name"), loc.BusinessName, loc.Address)
	mapsURL := prop(f.Properties, "google maps url", "url")
	lat, lon, hasCoords := 0.0, 0.0, false
	if len(f.Geometry.Coordinates) == 2 {
		var ok1, ok2 bool
		lon, ok1 = f.Geometry.Coordinates[0].Float()
		lat, ok2 = f.Geometry.Coordinates[1].Float()
		hasCoords = ok1 && ok2
	}
	if !hasCoords {
		var ok1, ok2 bool
		lat, ok1 = loc.Geo.Latitude.Float()
		lon, ok2 = loc.Geo.Longitude.Float()
		hasCoords = ok1 && ok2
	}
	if title == "" && mapsURL == "" && !hasCoords {
		return one(unknown(base, raw, "", nil)), nil
	}
	published, _ := rawTime(flex(prop(f.Properties, "published")), flex(prop(f.Properties, "updated")))

	ev := base
	ev.OccurredAt = published
	meta(ev, "url", mapsURL)
	meta(ev, "address", loc.Address)
	meta(ev, "business", loc.BusinessName)
	meta(ev, "country", loc.CountryCode)
	if hasCoords {
		metaAny(ev, "lat", lat)
		metaAny(ev, "lon", lon)
	}
	if u := prop(f.Properties, "updated"); u != "" {
		meta(ev, "updated", u)
	}
	key := firstNonEmpty(mapsURL, title)
	if key == "" {
		key = coord(lat) + "," + coord(lon)
	}

	rating := prop(f.Properties, "star rating", "rating")
	review := prop(f.Properties, "review comment", "comment", "review", "text")
	if rating != "" || review != "" {
		ev.Type, ev.Category = "google.maps.review", domain.CategoryContent
		ev.Title = "Reviewed " + firstNonEmpty(title, "a place")
		ev.Body = review
		meta(ev, "rating", rating)
		meta(ev, "place", title)
		ev.ExternalID = "g.maps.review:" + short(key)
		return one(ev), nil
	}

	ev.Type, ev.Category = "google.maps.saved", domain.CategorySave
	ev.Title = "Saved " + firstNonEmpty(title, "a place") + " in Google Maps"
	meta(ev, "place", title)
	if strings.Contains(strings.ToLower(path.Base(raw.Member)), "label") || strings.Contains(strings.ToLower(path.Base(raw.Member)), "markiert") {
		meta(ev, "list", "labeled")
		ev.Title = "Labeled " + firstNonEmpty(title, "a place") + " in Google Maps"
	}
	if mapsURL != "" {
		ev.Targets = tgt(domain.RelationSaved, short(mapsURL), mapsURL)
	}
	ev.ExternalID = "g.maps.saved:" + short(strings.ToLower(path.Base(raw.Member))+"\x00"+key)
	return one(ev), nil
}

// ---- Saved/<list>.csv ----

var savedListDirs = []string{"saved", "gespeichert", "enregistré", "enregistrés", "guardado", "guardados", "salvati"}

// savedListEx reads one Maps list ("Saved/Want to go.csv"): rows do not name
// their list, so each is emitted together with the file's stem, which also
// keeps the same place in two lists from deduplicating.
type savedListEx struct{}

func (savedListEx) Match(member string) bool {
	return stem(member, ".csv") != "" && parentIs(member, savedListDirs)
}

func (savedListEx) Extract(member string, r io.Reader, emit func(provider.RawRecord) error) error {
	name := path.Base(member)
	name = strings.TrimSpace(name[:len(name)-len(path.Ext(name))])
	return bundle.ReadCSV(r, func(row map[string]string) error {
		payload, err := json.Marshal(playlistRow{Playlist: name, Row: row})
		if err != nil {
			return err
		}
		return emit(rec("saved_list", payload))
	})
}

func normalizeSavedList(base domain.Event, raw provider.RawRecord) ([]domain.Event, error) {
	var pr playlistRow
	if err := json.Unmarshal(raw.Payload, &pr); err != nil {
		return nil, err
	}
	isURL := func(v string) bool { return strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://") }
	mapsURL := firstNonEmpty(field(pr.Row, "url"), sniff(pr.Row, isURL))
	title := field(pr.Row, "title")
	if title == "" {
		title = sniff(pr.Row, func(v string) bool { return v != "" && !isURL(v) })
	}
	if title == "" && mapsURL == "" {
		return one(unknown(base, raw, "", pr.Row)), nil
	}
	ev := base
	ev.Type, ev.Category = "google.maps.saved", domain.CategorySave
	ev.Title = "Saved " + firstNonEmpty(title, mapsURL) + " to " + firstNonEmpty(pr.Playlist, "a Maps list")
	meta(ev, "list", pr.Playlist)
	meta(ev, "place", title)
	meta(ev, "url", mapsURL)
	meta(ev, "note", firstNonEmpty(field(pr.Row, "note"), field(pr.Row, "notiz")))
	meta(ev, "comment", firstNonEmpty(field(pr.Row, "comment"), field(pr.Row, "kommentar")))
	if mapsURL != "" {
		ev.Targets = tgt(domain.RelationSaved, short(mapsURL), mapsURL)
	}
	ev.ExternalID = "g.maps.saved:" + short(strings.ToLower(pr.Playlist)+"\x00"+firstNonEmpty(mapsURL, title))
	return one(ev), nil // the lists carry no dates
}
