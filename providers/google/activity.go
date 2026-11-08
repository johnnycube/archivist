package google

import (
	"encoding/json"
	"net/url"
	"path"
	"strings"

	"github.com/johnnycube/archivist/internal/domain"
	"github.com/johnnycube/archivist/internal/provider"
)

// activity is one My Activity record. The schema is the same for every Google
// product ("My Activity/<Product>/MyActivity.json" and the YouTube history
// files); what differs is the product and the sentence in title.
type activity struct {
	Header    string `json:"header"`
	Title     string `json:"title"`
	TitleURL  string `json:"titleUrl"`
	Subtitles []struct {
		Name string `json:"name"`
		URL  string `json:"url"`
	} `json:"subtitles"`
	Time     string   `json:"time"`
	Products []string `json:"products"`
	Details  []struct {
		Name string `json:"name"`
	} `json:"details"`
	LocationInfos []struct {
		Name      string `json:"name"`
		URL       string `json:"url"`
		Source    string `json:"source"`
		SourceURL string `json:"sourceUrl"`
	} `json:"locationInfos"`
	ActivityControls []string `json:"activityControls"`
	Description      string   `json:"description"`
}

// productAliases maps a lower-cased header/products[]/folder name to a
// provider-local product key by substring. Order matters: "Image Search"
// before "Search", "Google Play Books" before "Google Play".
var productAliases = []struct {
	key   string
	names []string
}{
	{"youtube", []string{"youtube"}},
	{"image_search", []string{"image search", "bildersuche", "recherche d'images", "búsqueda de imágenes", "ricerca immagini"}},
	{"video_search", []string{"video search", "videosuche", "recherche de vidéos", "búsqueda de vídeos", "ricerca video"}},
	{"books", []string{"books", "bücher", "livres", "libros", "libri"}},
	{"play", []string{"play store", "google play"}},
	{"chrome", []string{"chrome"}},
	{"maps", []string{"maps"}},
	{"assistant", []string{"assistant", "assistent", "assistente"}},
	{"gemini", []string{"gemini", "bard"}},
	{"discover", []string{"discover", "news", "nachrichten", "actualités", "noticias", "notizie"}},
	{"ads", []string{"ads", "anzeigen", "annonces", "anuncios", "annunci"}},
	{"shopping", []string{"shopping"}},
	{"search", []string{"search", "suche", "recherche", "búsqueda", "ricerca", "zoeken", "pesquisa"}},
}

// productOf classifies a record by, in order, its header, its products[], the
// folder it was exported in, and the host of its URL.
func productOf(a activity, member string) string {
	candidates := append([]string{a.Header}, a.Products...)
	candidates = append(candidates, path.Base(path.Dir(member)))
	for _, c := range candidates {
		c = strings.ToLower(c)
		if c == "" {
			continue
		}
		for _, p := range productAliases {
			for _, n := range p.names {
				if strings.Contains(c, n) {
					return p.key
				}
			}
		}
	}
	if u, err := url.Parse(a.TitleURL); err == nil {
		host := strings.ToLower(u.Host)
		switch {
		case strings.Contains(host, "youtube") || host == "youtu.be":
			return "youtube"
		case strings.HasPrefix(host, "play.google"):
			return "play"
		case strings.HasPrefix(host, "maps.google") || strings.HasPrefix(u.Path, "/maps"):
			return "maps"
		case strings.HasPrefix(host, "gemini.google") || strings.HasPrefix(host, "bard.google"):
			return "gemini"
		case strings.HasPrefix(host, "news.google"):
			return "discover"
		case strings.Contains(host, "google") && u.Query().Has("q"):
			return "search"
		}
	}
	return ""
}

// verbFrames are the localized sentence frames My Activity puts around the
// object ("Searched for X", "X aufgerufen"). The first frame of each action is
// the English one. Only English and German were checked against real exports.
var verbFrames = []struct {
	action, prefix, suffix string
}{
	{"searched", "Searched for ", ""},
	{"searched", "Gesucht nach ", ""},
	{"searched", "Nach ", " gesucht"},
	{"searched", "Vous avez recherché ", ""},
	{"searched", "Buscaste ", ""},
	{"searched", "Hai cercato ", ""},
	{"visited", "Visited ", ""},
	{"visited", "", " aufgerufen"},
	{"visited", "Vous avez consulté ", ""},
	{"visited", "Visitaste ", ""},
	{"viewed", "Viewed ", ""},
	{"viewed", "", " angesehen"},
	{"used", "Used ", ""},
	{"used", "", " verwendet"},
	{"installed", "Installed ", ""},
	{"installed", "", " installiert"},
	{"updated", "Updated ", ""},
	{"updated", "", " aktualisiert"},
	{"prompted", "Prompted ", ""},
	{"said", "Said ", ""},
	{"asked", "Asked ", ""},
	{"directions", "Directions to ", ""},
	{"directions", "Got directions to ", ""},
	{"directions", "Route nach ", ""},
	{"clicked", "Clicked ", ""},
	{"clicked", "", " angeklickt"},
	{"listened", "Listened to ", ""},
	{"read", "Read ", ""},
	{"opened", "Opened ", ""},
	{"opened", "", " geöffnet"},
	{"watched", "Watched ", ""},
	{"purchased", "Purchased ", ""},
	{"purchased", "", " gekauft"},
}

// verbOf strips the sentence frame from title. english reports whether the
// frame was the English one (the localized ones are best-effort).
func verbOf(title string) (action, object string, english, ok bool) {
	for _, f := range verbFrames {
		if strings.HasPrefix(title, f.prefix) && strings.HasSuffix(title, f.suffix) && len(title) > len(f.prefix)+len(f.suffix) {
			return f.action, title[len(f.prefix) : len(title)-len(f.suffix)], isEnglishFrame(f.action, f.prefix), true
		}
	}
	return "", title, false, false
}

// isEnglishFrame: the first frame listed for an action is its English form.
func isEnglishFrame(action, prefix string) bool {
	for _, f := range verbFrames {
		if f.action == action {
			return f.prefix == prefix
		}
	}
	return false
}

// searchQuery reads the query of a Google search URL (q=), if any.
func searchQuery(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Query().Get("q")
}

func normalizeActivity(base domain.Event, raw provider.RawRecord) ([]domain.Event, error) {
	var a activity
	if err := json.Unmarshal(raw.Payload, &a); err != nil {
		return nil, err
	}
	product := productOf(a, raw.Member)
	if product == "youtube" {
		return normalizeYouTubeActivity(base, raw, a)
	}
	return one(mapActivity(base, raw, a, product)), nil
}

// mapActivity maps a non-YouTube My Activity record by product and verb.
func mapActivity(base domain.Event, raw provider.RawRecord, a activity, product string) domain.Event {
	action, object, english, framed := verbOf(a.Title)
	query := searchQuery(a.TitleURL)
	if query != "" {
		action = "searched"
	} else if action == "searched" {
		query = object
	}

	ev := activityBase(base, raw, a, action)
	switch product {
	case "search", "image_search", "video_search":
		if action != "searched" {
			return visitOrGeneric(base, raw, a, product)
		}
		ev.Type, ev.Category = "google.search", domain.CategoryConsumption
		ev.Body = query
		ev.Title = "Searched Google"
		switch product {
		case "image_search":
			ev.Title, ev.Metadata["kind"] = "Searched Google Images", "image"
		case "video_search":
			ev.Title, ev.Metadata["kind"] = "Searched Google Videos", "video"
		}
	case "chrome":
		switch {
		case action == "searched":
			ev.Type, ev.Category = "google.chrome.search", domain.CategoryConsumption
			ev.Body, ev.Title = query, "Searched from Chrome"
		case action == "visited" || a.TitleURL != "":
			ev.Type, ev.Category = "google.chrome.visit", domain.CategoryConsumption
			ev.Title = object
			if !english {
				meta(ev, "raw_title", a.Title)
			}
		default:
			return genericActivity(base, raw, a, product)
		}
	case "maps":
		ev.Type, ev.Category = "google.maps.activity", domain.CategoryConsumption
		ev.Title = a.Title
		if action == "searched" {
			ev.Body = query
		}
		if action == "directions" {
			meta(ev, "destination", object)
		}
	case "play":
		ev.Type, ev.Category = "google.play.activity", domain.CategoryConsumption
		ev.Title = a.Title
		if framed && action != "searched" {
			meta(ev, "app", object)
		}
	case "assistant":
		ev.Type, ev.Category = "google.assistant.query", domain.CategoryMessage
		ev.Title = "Asked Google Assistant"
		ev.Body = object // "Said set a timer" → "set a timer"; an unframed title is the utterance itself
		var response []string
		for _, s := range a.Subtitles {
			if s.Name != "" {
				response = append(response, s.Name)
			}
		}
		meta(ev, "response", strings.Join(response, "\n"))
	case "gemini":
		ev.Type, ev.Category = "google.gemini.prompt", domain.CategoryMessage
		ev.Title = "Prompted Gemini"
		ev.Body = object
	case "discover":
		ev.Type, ev.Category = "google.discover.view", domain.CategoryConsumption
		ev.Title = object
		if !english {
			meta(ev, "raw_title", a.Title)
		}
	case "ads":
		ev.Type, ev.Category = "google.ads.view", domain.CategoryOther
		ev.Title = a.Title
	case "shopping":
		ev.Type, ev.Category = "google.shopping.activity", domain.CategoryConsumption
		ev.Title = a.Title
		if action == "searched" {
			ev.Body = query
		}
	case "books":
		ev.Type, ev.Category = "google.books.activity", domain.CategoryConsumption
		ev.Title = a.Title
	default:
		return genericActivity(base, raw, a, product)
	}
	if ev.Title == "" {
		ev.Title = a.Title
	}
	ev.ExternalID = "g." + strings.TrimPrefix(ev.Type, "google.") + ":" + when(ev.OccurredAt, a.Time) + ":" + short(a.Header+"\x00"+a.TitleURL+"\x00"+a.Title)
	return ev
}

// visitOrGeneric maps "Visited X" (or an unframed title with a URL) to
// google.visit and anything else to google.activity. It serves Search, YouTube
// ("Visited YouTube Music") and products without a type of their own.
func visitOrGeneric(base domain.Event, raw provider.RawRecord, a activity, product string) domain.Event {
	action, object, english, framed := verbOf(a.Title)
	if action != "visited" && (a.TitleURL == "" || framed) {
		return genericActivity(base, raw, a, product)
	}
	ev := activityBase(base, raw, a, firstNonEmpty(action, "visited"))
	ev.Type, ev.Category = "google.visit", domain.CategoryConsumption
	ev.Title = firstNonEmpty(object, a.TitleURL)
	if !english {
		meta(ev, "raw_title", a.Title)
	}
	meta(ev, "product_key", product)
	ev.ExternalID = "g.visit:" + when(ev.OccurredAt, a.Time) + ":" + short(a.Header+"\x00"+a.TitleURL+"\x00"+a.Title)
	return ev
}

// genericActivity is the dated google.activity event for a product without a
// dedicated type (Takeout, Google Account, Help, Translate, …) or a sentence
// the product mapping does not understand. The product stays in metadata.
func genericActivity(base domain.Event, raw provider.RawRecord, a activity, product string) domain.Event {
	action, _, _, _ := verbOf(a.Title)
	ev := activityBase(base, raw, a, action)
	ev.Type, ev.Category = "google.activity", domain.CategoryOther
	ev.Title = firstNonEmpty(a.Title, "Google activity")
	meta(ev, "product_key", product)
	ev.ExternalID = "g.activity:" + when(ev.OccurredAt, a.Time) + ":" + short(a.Header+"\x00"+a.TitleURL+"\x00"+a.Title)
	return ev
}

// activityBase fills what every My Activity event shares.
func activityBase(base domain.Event, raw provider.RawRecord, a activity, action string) domain.Event {
	ev := base
	ev.OccurredAt = parseTime(a.Time)
	if ev.OccurredAt.IsZero() {
		meta(ev, "time", a.Time)
	}
	meta(ev, "product", firstNonEmpty(a.Header, strings.Join(a.Products, ", ")))
	meta(ev, "action", action)
	meta(ev, "url", a.TitleURL)
	meta(ev, "description", a.Description)
	var details []string
	for _, d := range a.Details {
		if contains(adMarkers, strings.ToLower(d.Name)) {
			ev.Metadata["ad"] = true
		} else if d.Name != "" {
			details = append(details, d.Name)
		}
	}
	meta(ev, "details", strings.Join(details, ", "))
	var subs []string
	for _, s := range a.Subtitles {
		if s.Name != "" {
			subs = append(subs, s.Name)
		}
	}
	meta(ev, "subtitles", strings.Join(subs, ", "))
	var locs []string
	for _, l := range a.LocationInfos {
		if l.Name != "" {
			locs = append(locs, l.Name)
		}
		if l.URL != "" {
			meta(ev, "location_url", l.URL)
		}
	}
	meta(ev, "location", strings.Join(locs, "; "))
	return ev
}
