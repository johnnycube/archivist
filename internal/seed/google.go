package seed

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/johnnycube/archivist/internal/domain"
)

// google: the Takeout — thirty event types from a dozen products. Two of
// them are the demo's high-volume consumption sources (YouTube watch history
// in evening bursts, Chrome history in browsing bursts); the rest is
// moderate: searches, Maps, location history, Keep, Chat, Photos. Shapes
// follow providers/google (Actor "self", "g.<type>:<when>:<hash>" ids, the
// My Activity metadata block, undated YouTube subscriptions).
func google(g *gen) {
	const src = "google"
	base := func(typ string, cat domain.Category, at time.Time) domain.Event {
		return domain.Event{SourceID: src, Type: typ, Category: cat, Actor: "self", OccurredAt: at, Metadata: map[string]any{}}
	}
	when := func(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
	gid := func(kind string, at time.Time, disc string) string {
		return fmt.Sprintf("g.%s:%s:%s", kind, when(at), short(disc))
	}
	videoID := func(seed string) string { return strings.ToLower(short(seed)[:11]) }
	tgt := func(rel domain.Relation, id, u string) []domain.Target {
		return []domain.Target{{Relation: rel, TargetSource: src, TargetExternalID: id, TargetURL: u}}
	}
	channelURL := func(ch string) string {
		return "https://www.youtube.com/channel/UC" + strings.ToUpper(short(ch)[:16]) + "aaaaaa"
	}

	// ---- YouTube: watch history in evening bursts (~1,500), searches, comments, subscriptions, playlists, likes ----
	watch := func(at time.Time) {
		v := videos[g.rng.Intn(len(videos))]
		vid := videoID(v.Title)
		u := "https://www.youtube.com/watch?v=" + vid
		ev := base("google.youtube.watch", domain.CategoryConsumption, at)
		ev.ExternalID, ev.Title = "yt.watch:"+when(at)+":"+short(u), v.Title
		ev.Metadata["product"], ev.Metadata["url"], ev.Metadata["video_id"], ev.Metadata["channel"], ev.Metadata["channel_url"] = "YouTube", u, vid, v.Channel, channelURL(v.Channel)
		if g.chance(0.04) {
			ev.Metadata["details"], ev.Metadata["ad"] = "From Google Ads", true
		}
		g.add(ev)
	}
	burst := func(at time.Time, n int) {
		for i := 0; i < n; i++ {
			watch(at)
			at = at.Add(g.minutes(6, 22))
		}
	}
	burst(g.clock(g.now.AddDate(0, 0, -1), 20, 21), 9) // last night
	for w := 104; w >= 1; w-- {                        // two years of evenings, ~2 bursts a week
		week := g.now.Add(-time.Duration(w) * 7 * 24 * time.Hour)
		for b := 0; b < g.between(1, 3); b++ {
			day := week.Add(-time.Duration(g.rng.Intn(7)) * 24 * time.Hour)
			burst(g.clock(day, 19, 23), g.between(4, 14))
		}
	}
	for i := 0; i < 80; i++ {
		at := g.since(2016)
		q := g.pick(youtubeSearches)
		ev := base("google.youtube.search", domain.CategoryConsumption, at)
		ev.ExternalID, ev.Title, ev.Body = "yt.search:"+when(at)+":"+short(q+fmt.Sprint(i)), "Searched YouTube", q
		ev.Metadata["product"], ev.Metadata["url"] = "YouTube", "https://www.youtube.com/results?search_query="+url.QueryEscape(q)
		g.add(ev)
	}
	var comments []string
	for i := 0; i < 15; i++ {
		at := g.since(2017)
		v := videos[g.rng.Intn(len(videos))]
		vid, cid := videoID(v.Title), "Ugx"+strings.ToUpper(short(fmt.Sprint("ytc", i))[:16])+"AaAg"
		ev := base("google.youtube.comment", domain.CategoryContent, at)
		ev.ExternalID, ev.Title, ev.Body = "yt.comment:"+cid, "Commented on a YouTube video", g.pick([]string{"See 2:31 for the trick, saved me an hour.", "Finally someone explains this properly. Subscribed.", "Tried this on my 1978 wheel, worked first time.", "The crumb shot at the end 😍", "Great pacing advice, used it for my half.", "Which mic are you using? Sounds great."})
		u := fmt.Sprintf("https://www.youtube.com/watch?v=%s&lc=%s", vid, cid)
		ev.Targets = tgt(domain.RelationRepliedTo, vid, u)
		ev.Metadata["url"], ev.Metadata["video_id"], ev.Metadata["channel_id"] = u, vid, channelURL(v.Channel)[32:]
		if i%4 == 3 && len(comments) > 0 {
			parent := comments[g.rng.Intn(len(comments))]
			ev.Title, ev.ParentExternalID, ev.Body = "Replied to a YouTube comment", "yt.comment:"+parent, "Replying to @someone: same here!"
			ev.Targets = tgt(domain.RelationRepliedTo, parent, u)
		}
		comments = append(comments, cid)
		g.add(ev)
	}
	channels := map[string]bool{}
	for _, v := range videos {
		if channels[v.Channel] || len(channels) >= 20 {
			continue
		}
		channels[v.Channel] = true
		cu := channelURL(v.Channel)
		ev := base("google.youtube.subscribe", domain.CategoryConnection, time.Time{}) // the export does not say when
		ev.ExternalID, ev.Title = "yt.subscribe:"+cu[32:], "Subscribed to "+v.Channel
		ev.Targets = tgt(domain.RelationFollowed, cu[32:], cu)
		ev.Metadata["channel"], ev.Metadata["channel_id"], ev.Metadata["channel_url"] = v.Channel, cu[32:], cu
		g.add(ev)
	}
	for i, pl := range []string{"Watch later", "Watch later", "Watch later", "Bike repair", "Bike repair", "Talks to rewatch", "Running form", "Kyoto", "Watch later", "Talks to rewatch", "Bread", "Bread", "Watch later", "Homelab", "Homelab", "Watch later", "Bike repair", "Kyoto", "Watch later", "Talks to rewatch", "Running form", "Watch later", "Bread", "Homelab", "Watch later"} {
		v := videos[(i*7)%len(videos)]
		vid := videoID(v.Title)
		ev := base("google.youtube.playlist_item", domain.CategorySave, g.since(2018))
		ev.ExternalID, ev.Title = fmt.Sprintf("yt.playlist_item:%s:%s", short(pl), vid), "Saved a video to "+pl
		ev.Targets = tgt(domain.RelationSaved, vid, "https://www.youtube.com/watch?v="+vid)
		ev.Metadata["playlist"], ev.Metadata["video_id"], ev.Metadata["url"] = pl, vid, "https://www.youtube.com/watch?v="+vid
		g.add(ev)
	}
	for i := 0; i < 40; i++ {
		v := videos[(i*3)%len(videos)]
		vid := videoID(v.Title + fmt.Sprint(i/len(videos)))
		ev := base("google.youtube.like", domain.CategoryReaction, g.since(2017))
		ev.ExternalID, ev.Title = "yt.like:"+vid, "Liked a YouTube video"
		ev.Targets = tgt(domain.RelationLiked, vid, "https://www.youtube.com/watch?v="+vid)
		ev.Metadata["playlist"], ev.Metadata["video_id"], ev.Metadata["url"] = "Liked videos", vid, "https://www.youtube.com/watch?v="+vid
		g.add(ev)
	}

	// ---- My Activity: Search, Chrome, Maps, Play, Assistant, Gemini, Discover, Ads, Shopping, Books, generic ----
	activity := func(typ string, cat domain.Category, at time.Time, product, action, title string) domain.Event {
		ev := base(typ, cat, at)
		ev.Title = title
		ev.Metadata["product"], ev.Metadata["action"] = product, action
		ev.ExternalID = gid(strings.TrimPrefix(typ, "google."), at, product+"\x00"+title)
		return ev
	}
	for i := 0; i < 200; i++ {
		at := g.since(2015)
		q := g.pick(googleSearches)
		ev := activity("google.search", domain.CategoryConsumption, at, "Search", "searched", "Searched Google")
		ev.Body, ev.Metadata["url"] = q, "https://www.google.com/search?q="+url.QueryEscape(q)
		ev.ExternalID = gid("search", at, "Search\x00"+q+fmt.Sprint(i))
		if i%9 == 8 {
			ev.Title, ev.Metadata["product"], ev.Metadata["kind"] = "Searched Google Images", "Image Search", "image"
		}
		if g.chance(0.3) {
			ev.Metadata["location"], ev.Metadata["location_url"] = "At this general area - From your places (Home)", "https://www.google.com/maps/@?api=1&map_action=map&center=52.503,13.394&zoom=12"
		}
		g.add(ev)
	}
	for i := 0; i < 60; i++ {
		at := g.since(2015)
		s := sites[g.rng.Intn(len(sites))]
		ev := activity("google.visit", domain.CategoryConsumption, at, "Search", "visited", s.Title)
		ev.ExternalID = gid("visit", at, "Search\x00"+s.URL+fmt.Sprint(i))
		ev.Metadata["url"], ev.Metadata["product_key"] = s.URL, "search"
		g.add(ev)
	}
	// Chrome: browsing bursts (~1,500), from Chrome/History.json
	for w := 52; w >= 1; w-- {
		week := g.now.Add(-time.Duration(w) * 7 * 24 * time.Hour)
		for b := 0; b < g.between(1, 3); b++ {
			day := week.Add(-time.Duration(g.rng.Intn(7)) * 24 * time.Hour)
			at := g.clock(day, 8, 23)
			client := g.pick([]string{"ThinkPad", "Pixel", "MacBook"})
			for i, n := 0, g.between(5, 25); i < n; i++ {
				s := sites[g.rng.Intn(len(sites))]
				ev := base("google.chrome.visit", domain.CategoryConsumption, at)
				ev.ExternalID, ev.Title = "g.chrome.visit:"+when(at)+":"+short(s.URL), s.Title
				ev.Metadata["url"], ev.Metadata["transition"], ev.Metadata["client_id"], ev.Metadata["origin"] = s.URL, g.pick([]string{"LINK", "LINK", "TYPED", "GENERATED", "RELOAD", "AUTO_BOOKMARK"}), client, "chrome_history"
				g.add(ev)
				at = at.Add(time.Duration(g.between(20, 400)) * time.Second)
			}
		}
	}
	for i := 0; i < 60; i++ {
		at := g.since(2016)
		q := g.pick(googleSearches)
		ev := activity("google.chrome.search", domain.CategoryConsumption, at, "Chrome", "searched", "Searched from Chrome")
		ev.Body, ev.Metadata["url"] = q, "https://www.google.com/search?q="+url.QueryEscape(q)
		ev.ExternalID = gid("chrome.search", at, "Chrome\x00"+q+fmt.Sprint(i))
		g.add(ev)
	}
	for i := 0; i < 40; i++ {
		at := g.since(2016)
		pl := places[2+g.rng.Intn(len(places)-2)]
		ev := activity("google.maps.activity", domain.CategoryConsumption, at, "Maps", "searched", "Searched for "+strings.ToLower(pl.Name))
		ev.Body = strings.ToLower(pl.Name)
		switch i % 3 {
		case 1:
			ev.Title, ev.Body, ev.Metadata["action"], ev.Metadata["destination"] = "Directions to "+pl.Name, "directions to "+pl.Name, "directions", pl.Name
		case 2:
			ev.Title, ev.Body, ev.Metadata["action"] = "Viewed area around "+pl.Name, "area around "+pl.Name, "viewed"
		}
		ev.ExternalID = gid("maps.activity", at, "Maps\x00"+ev.Title+fmt.Sprint(i))
		ev.Metadata["url"] = fmt.Sprintf("https://www.google.com/maps/search/?api=1&query=%.4f,%.4f", pl.Lat, pl.Lon)
		g.add(ev)
	}
	for i, app := range []string{"Example Fitness Tracker", "Komoot", "Slow Loaf Timer", "Spotify", "Archivist Companion", "Duolingo", "Signal", "Kilometre Club", "Strava", "Bean Counter", "DB Navigator", "BVG Fahrinfo", "Table Talk Companion", "Firefox", "Nextcloud", "Home Assistant", "Kyoto Metro", "Komoot", "Signal", "Spotify"} {
		at := g.since(2016)
		verb := []string{"Installed", "Updated", "Used"}[i%3]
		ev := activity("google.play.activity", domain.CategoryConsumption, at, "Google Play Store", strings.ToLower(verb), verb+" "+app)
		ev.ExternalID = gid("play.activity", at, "Google Play Store\x00"+ev.Title+fmt.Sprint(i))
		ev.Metadata["app"] = app
		g.add(ev)
	}
	for i := 0; i < 30; i++ {
		at := g.since(2018)
		q := g.pick([]string{"set a timer for ten minutes", "what's the weather tomorrow", "play the paper lanterns", "remind me to water the tomatoes at 7", "how many kilometres is a half marathon", "turn off the living room lights", "translate good morning to japanese", "call max", "add flour to the shopping list", "how long do I bake bread at 250"})
		ev := activity("google.assistant.query", domain.CategoryMessage, at, "Assistant", "said", "Asked Google Assistant")
		ev.Body, ev.ExternalID = q, gid("assistant.query", at, "Assistant\x00"+q+fmt.Sprint(i))
		ev.Metadata["response"] = g.pick([]string{"Sure, ten minutes, starting now.", "Tomorrow in Berlin: 14 degrees and cloudy.", "Playing The Paper Lanterns on Spotify.", "OK, I'll remind you at 7 PM.", "A half marathon is 21.1 kilometres.", "Turning off two lights.", "おはようございます", "Calling Max Mustermann.", "Added flour to your shopping list.", "Typically 30 to 40 minutes at 250 degrees."})
		g.add(ev)
	}
	for i := 0; i < 25; i++ {
		at := g.since(2024)
		q := g.pick([]string{"Explain how to true a bicycle wheel in three steps", "Draft a polite email to my landlord about the heating", "Summarise the rules of Brass: Birmingham", "Give me a 4-day Kyoto itinerary in autumn", "What's the difference between a flat white and a latte", "Plan a vegetarian week of dinners", "Explain log scales to a 10-year-old", "Rewrite this toast to be shorter", "What does 'idempotent' mean", "Suggest names for a cat that opens doors"})
		ev := activity("google.gemini.prompt", domain.CategoryMessage, at, "Gemini Apps", "prompted", "Prompted Gemini")
		ev.Body, ev.ExternalID = q, gid("gemini.prompt", at, "Gemini Apps\x00"+q+fmt.Sprint(i))
		g.add(ev)
	}
	for i := 0; i < 40; i++ {
		at := g.since(2019)
		hl := g.pick([]string{"Synthetic Gazette: Bike lanes expand downtown", "Example Times: The return of the night train", "Placeholder Post: Why sourdough never left", "Sample Herald: Berlin half marathon draws record field", "Synthetic Gazette: Self-hosting is having a moment", "Example Times: A short history of the espresso machine", "Placeholder Post: The best autumn walks in Kyoto", "Sample Herald: Board games outsell video games in cafés"})
		ev := activity("google.discover.view", domain.CategoryConsumption, at, "Discover", "viewed", hl)
		ev.ExternalID = gid("discover.view", at, "Discover\x00"+hl+fmt.Sprint(i))
		ev.Metadata["url"] = "https://news.example.com/" + strings.ToLower(strings.ReplaceAll(strings.SplitN(hl, ": ", 2)[1], " ", "-"))
		g.add(ev)
	}
	for i := 0; i < 15; i++ {
		at := g.since(2017)
		ad := g.pick([]string{"Example Phone", "Northwind Running Shoes", "Contoso Cloud Backup", "Acme Espresso Grinder", "Fabrikam Robot Vacuum", "Globex Streaming"})
		ev := activity("google.ads.view", domain.CategoryOther, at, "Ads", "viewed", "Viewed ad for "+ad)
		ev.ExternalID = gid("ads.view", at, "Ads\x00"+ad+fmt.Sprint(i))
		ev.Metadata["details"], ev.Metadata["ad"] = "From Google Ads", true
		g.add(ev)
	}
	for i := 0; i < 15; i++ {
		at := g.since(2019)
		q := g.pick([]string{"spoke wrench", "bread flour 25kg", "trail running shoes wide", "mechanical keyboard silent", "monitor arm", "hand grinder", "grow light balcony", "hiking poles"})
		ev := activity("google.shopping.activity", domain.CategoryConsumption, at, "Shopping", "searched", "Searched for "+q)
		ev.Body, ev.ExternalID = q, gid("shopping.activity", at, "Shopping\x00"+q+fmt.Sprint(i))
		g.add(ev)
	}
	for i := 0; i < 8; i++ {
		at := g.since(2018)
		ev := activity("google.books.activity", domain.CategoryConsumption, at, "Google Play Books", "used", "Used Google Play Books")
		ev.ExternalID = gid("books.activity", at, "Google Play Books\x00"+fmt.Sprint(i))
		if i%2 == 1 {
			ev.Title, ev.Metadata["action"] = "Read "+g.pick([]string{"Salt: A World History", "The Long Run", "Bread Science", "Designing Data-Intensive Applications"}), "read"
		}
		g.add(ev)
	}
	for i := 0; i < 20; i++ {
		at := g.since(2015)
		t := g.pick([]string{"Created an export", "Used Search", "Used Translate", "Signed in to Google Account", "Changed account password", "Used Google Account", "Downloaded an export", "Used Takeout"})
		ev := activity("google.activity", domain.CategoryOther, at, g.pick([]string{"Takeout", "Account", "Translate"}), "used", t)
		ev.ExternalID = gid("activity", at, t+fmt.Sprint(i))
		ev.Metadata["product_key"] = strings.ToLower(ev.Metadata["product"].(string))
		g.add(ev)
	}

	// ---- Chrome bookmarks ----
	folders := []string{"Bookmarks bar", "Bookmarks bar/Bikes & Gear", "Bookmarks bar/Dev", "Other bookmarks/Recipes", "Other bookmarks/Running", "Other bookmarks"}
	for i := 0; i < 30; i++ {
		s := sites[(i*7)%len(sites)] // 7 is coprime to the site count: 30 distinct pages
		folder := folders[i%len(folders)]
		ev := base("google.chrome.bookmark", domain.CategorySave, g.since(2015))
		ev.ExternalID, ev.Title = "g.chrome.bookmark:"+short(folder+"\x00"+s.URL), "Bookmarked "+s.Title
		ev.Targets = tgt(domain.RelationSaved, short(s.URL), s.URL)
		ev.Metadata["url"], ev.Metadata["name"], ev.Metadata["folder"], ev.Metadata["modified"] = s.URL, s.Title, folder, rfc3339(ev.OccurredAt.Add(48*time.Hour))
		g.add(ev)
	}

	// ---- Location History: visits, trips between them, raw points ----
	for i := 0; i < 80; i++ {
		pl := places[g.rng.Intn(len(places))]
		if i%3 == 0 {
			pl = places[i%2] // home and work dominate, as they do
		}
		at := g.clock(g.since(2016), 7, 21)
		dur := int64(g.between(20, 240))
		ev := base("google.location.visit", domain.CategoryOther, at)
		ev.ExternalID, ev.Title = fmt.Sprintf("g.location.visit:%s:%s", when(at), short(fmt.Sprintf("%.7f,%.7f", pl.Lat, pl.Lon))), "Visited "+pl.Name
		ev.Metadata["name"], ev.Metadata["address"], ev.Metadata["place_id"] = pl.Name, pl.Address, "ChIJ"+strings.ToUpper(short(pl.Name)[:16])+"000001"
		ev.Metadata["lat"], ev.Metadata["lon"], ev.Metadata["end"], ev.Metadata["duration_min"], ev.Metadata["confidence"] = pl.Lat, pl.Lon, rfc3339(at.Add(time.Duration(dur)*time.Minute)), dur, g.pick([]string{"HIGH_CONFIDENCE", "MEDIUM_CONFIDENCE", "USER_CONFIRMED"})
		if pl.Type != "" {
			ev.Metadata["semantic_type"] = pl.Type
		}
		g.add(ev)
		if i%4 == 1 { // the trip that got her there
			from := places[g.rng.Intn(len(places))]
			act := g.pick([]string{"CYCLING", "CYCLING", "WALKING", "IN_PASSENGER_VEHICLE", "IN_TRAIN", "IN_BUS", "RUNNING"})
			dist := int64(g.between(600, 12000))
			if from.Lat < 50 || pl.Lat < 50 {
				act, dist = "IN_TRAIN", int64(g.between(200000, 900000))
			}
			tdur := int64(g.between(8, 90))
			trip := base("google.location.trip", domain.CategoryOther, at.Add(-time.Duration(tdur)*time.Minute))
			trip.ExternalID = fmt.Sprintf("g.location.trip:%s:%s", when(trip.OccurredAt), short(act+"\x00"+fmt.Sprint(dist)))
			trip.Title = fmt.Sprintf("Traveled %s (%.1f km)", strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(act, "IN_"), "_", " ")), float64(dist)/1000)
			if strings.HasPrefix(act, "IN_") {
				trip.Title = fmt.Sprintf("Traveled in %s (%.1f km)", strings.ToLower(strings.ReplaceAll(strings.TrimPrefix(act, "IN_"), "_", " ")), float64(dist)/1000)
			}
			trip.Metadata["distance_m"], trip.Metadata["activity_type"], trip.Metadata["end"], trip.Metadata["duration_min"] = dist, act, rfc3339(at), tdur
			trip.Metadata["start_lat"], trip.Metadata["start_lon"], trip.Metadata["end_lat"], trip.Metadata["end_lon"], trip.Metadata["confidence"] = from.Lat, from.Lon, pl.Lat, pl.Lon, "HIGH"
			g.add(trip)
		}
	}
	for d := 0; d < 6; d++ { // a few days of raw points (a morning run's worth each)
		at := g.clock(g.since(2022), 6, 8)
		lat, lon := places[0].Lat, places[0].Lon
		for i := 0; i < 25; i++ {
			lat += (g.rng.Float64() - 0.5) * 0.002
			lon += (g.rng.Float64() - 0.5) * 0.003
			ev := base("google.location.point", domain.CategoryOther, at)
			ev.ExternalID, ev.Title = fmt.Sprintf("g.location.point:%s:%.7f,%.7f", when(at), lat, lon), fmt.Sprintf("Location %.7f, %.7f", lat, lon)
			ev.Metadata["lat"], ev.Metadata["lon"], ev.Metadata["source"], ev.Metadata["accuracy_m"], ev.Metadata["altitude_m"], ev.Metadata["device"], ev.Metadata["platform"] = lat, lon, g.pick([]string{"GPS", "GPS", "WIFI"}), float64(g.between(4, 30)), float64(g.between(34, 60)), "-1234567890", "ANDROID"
			g.add(ev)
			at = at.Add(time.Duration(g.between(40, 120)) * time.Second)
		}
	}

	// ---- Maps: saved places and reviews ----
	for i, pl := range places[2:17] {
		u := fmt.Sprintf("https://www.google.com/maps/place/?q=place_id:ChIJ%s", strings.ToUpper(short(pl.Name)[:16]))
		ev := base("google.maps.saved", domain.CategorySave, g.since(2018))
		ev.ExternalID, ev.Title = "g.maps.saved:"+short("saved places.json\x00"+u), "Saved "+pl.Name+" in Google Maps"
		ev.Targets = tgt(domain.RelationSaved, short(u), u)
		ev.Metadata["url"], ev.Metadata["address"], ev.Metadata["business"], ev.Metadata["country"], ev.Metadata["lat"], ev.Metadata["lon"], ev.Metadata["place"] = u, pl.Address, pl.Name, "DE", pl.Lat, pl.Lon, pl.Name
		if pl.Lat < 50 {
			ev.Metadata["country"] = map[bool]string{true: "JP", false: "GB"}[pl.Lon > 100]
		}
		if i%5 == 4 { // from a list CSV: undated, with a note
			ev.OccurredAt = time.Time{}
			ev.ExternalID, ev.Title = "g.maps.saved:"+short("want to go\x00"+u), "Saved "+pl.Name+" to Want to go"
			ev.Metadata = map[string]any{"list": "Want to go", "place": pl.Name, "url": u, "note": g.pick([]string{"Try the rye", "Max says the espresso is good", "Open late on Fridays", "Bring cash"})}
		}
		g.add(ev)
	}
	for i, r := range []struct {
		pl     int
		rating string
		text   string
	}{
		{2, "5", "Trued my wheel while I waited. Friendly staff, fair price."},
		{3, "5", "Best rye in the neighbourhood. Get there before 10."},
		{4, "4", "Excellent beans, slightly chaotic queue on weekends."},
		{16, "4", "Great selection of games, coffee is fine. Book a table for five."},
		{11, "5", "Saw Marlowe Vance here — sound was perfect."},
		{18, "3", "Competent, but waiting times are long and parking is impossible."},
	} {
		pl := places[r.pl]
		u := fmt.Sprintf("https://www.google.com/maps/place/?q=place_id:ChIJ%s", strings.ToUpper(short(pl.Name)[:16]))
		ev := base("google.maps.review", domain.CategoryContent, g.since(2019))
		ev.ExternalID, ev.Title, ev.Body = "g.maps.review:"+short(u), "Reviewed "+pl.Name, r.text
		ev.Metadata["url"], ev.Metadata["address"], ev.Metadata["business"], ev.Metadata["country"], ev.Metadata["lat"], ev.Metadata["lon"], ev.Metadata["rating"], ev.Metadata["place"] = u, pl.Address, pl.Name, "DE", pl.Lat, pl.Lon, r.rating, pl.Name
		_ = i
		g.add(ev)
	}

	// ---- Keep ----
	for i, n := range []struct{ title, body string }{
		{"Groceries", "[x] Milk\n[ ] Spokes (36)\n[x] Bread flour 5kg\n[ ] Cat food"},
		{"Idea", "Timeline of everything I ever posted. One binary. Postgres. Import the exports, never drop data."},
		{"Packing list — hut trek", "[x] Rain jacket\n[x] Headlamp\n[ ] Sleeping bag liner\n[x] Cash for the hut\n[ ] Blister plasters"},
		{"Sourdough timing", "Feed 8am, mix 12pm, fold every 30 min until 3pm, shape 6pm, fridge overnight, bake 8am."},
		{"Books to read", "[ ] Salt: A World History\n[x] Designing Data-Intensive Applications\n[ ] The Long Run"},
		{"Kyoto", "Arashiyama at 7am. Fushimi Inari from the top down. Nishiki market for lunch. JR pass covers Nara."},
		{"Untitled note", "kernel vet 14:30 thursday"},
		{"Marathon plan", "Weeks 1–4 base, 5–8 build, 9–11 peak, 12 taper. Long runs Sunday. Gel at 7k and 14k on race day."},
		{"Wifi", "Flat 4B guest: see the fridge"},
		{"Talk outline", "1. Exports are APIs\n2. One event to rule them all\n3. Never drop data\n4. Demo"},
		{"Grandpa's 90th", "Jane: cake. Erika: salad. Max: drinks. Photo at 4pm, four generations."},
		{"Espresso dial-in", "18g in / 36g out / 28s. Grind 2 clicks finer. Still sour → fresher beans."},
		{"Balcony", "North-facing. Cherry tomatoes only. Grow light 6h."},
		{"Blog ideas", "Keyset pagination war story. Writing a PDF by hand. The mojibake incident."},
		{"Untitled note", "203.0.113.7 is the demo IP, do not reuse"},
	} {
		at := g.since(2016)
		ev := base("google.keep.note", domain.CategoryContent, at)
		ev.ExternalID, ev.Title, ev.Body = fmt.Sprintf("g.keep.note:%d", at.UnixMicro()), n.title, n.body
		if strings.Contains(n.body, "[ ]") || strings.Contains(n.body, "[x]") {
			ev.Metadata["list"] = true
		}
		ev.Metadata["edited"] = rfc3339(at.Add(time.Duration(g.between(1, 300)) * time.Hour))
		if i%4 == 0 {
			ev.Metadata["pinned"] = true
		}
		if i%3 == 1 {
			ev.Metadata["color"], ev.Metadata["labels"] = g.pick([]string{"BLUE", "GREEN", "YELLOW"}), g.pick([]string{"Shopping", "Trips", "Baking", "Work"})
		}
		if i == 1 {
			ev.Media = []domain.Media{g.picture(src, "Takeout/Keep/Idea.png", styleBlocks)}
		}
		g.add(ev)
	}

	// ---- Chat and Hangouts ----
	for _, space := range []struct {
		name   string
		dm     bool
		people []person
	}{{"Space AAAAsynthetic1", false, friends[:3]}, {"DM 7Lk2synthetic", true, friends[3:4]}} {
		dir := "Takeout/Google Chat/Groups/" + space.name
		for i, l := range g.conversation(g.since(2021), space.people, g.between(15, 25)) {
			ev := base("google.chat.message", domain.CategoryMessage, l.At)
			who := self.Name
			if !l.Self {
				who = l.Who.Name
			}
			ev.ExternalID, ev.Actor, ev.Body, ev.ThreadExternalID = fmt.Sprintf("g.chat.message:msg-%s-%04d", short(space.name)[:6], i), who, l.Body, "g.chat:"+space.name
			ev.Title = "Message in " + space.name
			if space.dm {
				ev.Title = "Direct message"
			}
			ev.Metadata["group"], ev.Metadata["email"], ev.Metadata["topic_id"], ev.Metadata["state"] = space.name, strings.ToLower(strings.ReplaceAll(who, " ", "."))+"@example.com", fmt.Sprintf("topic-%d", 1+i/6), "PRESENT"
			if l.Body == "Photo" {
				ev.Body, ev.Metadata["attachments"] = "route for saturday", "route.png"
				ev.Media = []domain.Media{g.picture(src, dir+"/route.png", styleLandscape)}
			}
			if g.chance(0.1) {
				ev.Metadata["reactions"] = g.pick([]string{"👍", "😂", "❤️"})
			}
			g.add(ev)
		}
	}
	for c, conv := range []struct {
		name   string
		people []person
	}{{"Uni reunion", cast[5:8]}, {"", friends[:1]}} {
		convID := "Ugx" + strings.ToUpper(short(conv.name + fmt.Sprint(c))[:16]) + "conv"
		title := "Message in " + conv.name
		if conv.name == "" {
			title = "Hangouts with " + conv.people[0].Name + ", " + self.Name
		}
		start := g.uniform(time.Date(2015, 3, 1, 0, 0, 0, 0, time.UTC), time.Date(2019, 6, 1, 0, 0, 0, 0, time.UTC))
		for i, l := range g.conversation(start, conv.people, g.between(12, 20)) {
			ev := base("google.hangouts.message", domain.CategoryMessage, l.At)
			who := self.Name
			if !l.Self {
				who = l.Who.Name
			}
			ev.ExternalID, ev.Actor, ev.Title, ev.Body, ev.ThreadExternalID = fmt.Sprintf("g.hangouts.message:evt-%s-%04d", short(convID)[:6], i), who, title, l.Body, "g.hangouts:"+convID
			ev.Metadata["conversation"], ev.Metadata["conversation_id"] = conv.name, convID
			if conv.name == "" {
				delete(ev.Metadata, "conversation")
			}
			g.add(ev)
		}
	}

	// ---- Photos ----
	albums := []string{"Photos from 2019", "Photos from 2022", "Photos from 2024", "Photos from 2026", "Kyoto"}
	for i := 0; i < 30; i++ {
		album := albums[i%len(albums)]
		var at time.Time
		if strings.HasPrefix(album, "Photos from ") {
			y := 2019
			fmt.Sscanf(album, "Photos from %d", &y)
			end := time.Date(y, 12, 31, 0, 0, 0, 0, time.UTC)
			if end.After(g.now) {
				end = g.now
			}
			at = g.uniform(time.Date(y, 1, 1, 0, 0, 0, 0, time.UTC), end)
		} else {
			at = g.uniform(time.Date(2025, 11, 8, 0, 0, 0, 0, time.UTC), time.Date(2025, 11, 12, 0, 0, 0, 0, time.UTC))
		}
		name := fmt.Sprintf("PXL_%s.png", at.Format("20060102_150405000"))
		member := "Takeout/Google Photos/" + album + "/" + name
		pl := places[g.rng.Intn(len(places))]
		ev := base("google.photos.photo", domain.CategoryMedia, at)
		ev.ExternalID, ev.Title, ev.Body = "g.photos.photo:"+short(member), name, g.pick([]string{"Freshly trued wheel", "Golden hour over the Spree", "Kernel on the NAS again", "Hut breakfast, day 2", "Finish line", "Crumb shot", "Bamboo grove before the crowds", "Grandpa's 90th — four generations", "Balcony, first tomato", "Board game night"})
		ev.Metadata["url"], ev.Metadata["album"], ev.Metadata["uploaded"], ev.Metadata["lat"], ev.Metadata["lon"], ev.Metadata["origin"], ev.Metadata["device"] = "https://photos.google.com/photo/"+short(member), album, rfc3339(at.Add(time.Hour)), pl.Lat, pl.Lon, "mobileUpload", "ANDROID_PHONE"
		if g.chance(0.3) {
			ev.Metadata["people"], ev.Metadata["views"] = g.pick([]string{"Max Mustermann", "Erika Mustermann", "Max Mustermann, Erika Mustermann", "Alice Example"}), int64(g.between(1, 40))
		}
		if i%6 == 0 {
			ev.Metadata["favorited"] = true
		}
		if i < 5 {
			ev.Media = []domain.Media{g.picture(src, member, pictureStyle((i+2)%4))}
			ev.Media[0].URL = ""
		} else {
			ev.Media = []domain.Media{{BlobKey: member, URL: ev.Metadata["url"].(string)}}
		}
		g.add(ev)
	}

	// ---- unknown: the four shapes a Takeout produces ----
	g.add(g.unknown(src, "Takeout/Mail/All mail Including Spam and Trash.mbox", "From 1712345678901234567@xxx Mon Sep 01 08:12:44 +0000 2026\nX-GM-THRID: 1712345678901234567\nX-Gmail-Labels: Inbox,Important\n", 4123456789))
	g.add(g.unknown(src, "Takeout/Drive/Talk outline.docx", "PK\x03\x04", 48213))
	g.add(g.unknown(src, "Takeout/archive_browser.html", `<!DOCTYPE html><html lang="en"><head><meta charset="utf-8"><title>Google Takeout</title>`, 22190))
	g.add(domain.Event{
		SourceID: src, ExternalID: "google:unknown:Takeout/Timeline/Timeline.json#userLocationProfile.frequentPlaces", Type: "google.unknown", Category: domain.CategoryOther,
		Title:    `Unmapped export data: Takeout/Timeline/Timeline.json ("userLocationProfile.frequentPlaces", 3 records)`,
		Metadata: map[string]any{"member": "Takeout/Timeline/Timeline.json", "key": "userLocationProfile.frequentPlaces", "count": 3},
	})
}
