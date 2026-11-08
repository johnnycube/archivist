package seed

import (
	"fmt"
	"strings"
	"time"

	"github.com/johnnycube/archivist/internal/domain"
)

// spotify: the demo's biggest source by far. Listening sessions — bursts of
// 8–40 tracks a few minutes apart, several a week for the last ~14 months
// and sparser before — so that the timeline's clustering and the overview's
// log scale have something to show. Shapes follow providers/spotify
// (Actor "self", "<artist> — <track>" titles, ms_played and the extended
// streaming-history keys).
func spotify(g *gen) {
	const src = "spotify"
	const member = "Spotify Extended Streaming History/Streaming_History_Audio_2018-2026.json"
	base := func(typ string, cat domain.Category, at time.Time) domain.Event {
		return domain.Event{SourceID: src, Type: typ, Category: cat, Actor: "self", OccurredAt: at, Metadata: map[string]any{"member": member}}
	}
	uri := func(kind, seed string) string {
		return fmt.Sprintf("spotify:%s:%s%06d", kind, strings.ToUpper(short(seed)[:16]), hash64(seed)%1e6)
	}
	trackURI := func(i int) string { return uri("track", songs[i].Track+songs[i].Artist) }
	open := func(u string) string {
		p := strings.Split(u, ":")
		return "https://open.spotify.com/" + p[1] + "/" + p[2]
	}

	// one session: an album run or a shuffled mix, tracks back to back
	session := func(at time.Time, n int) {
		album := g.chance(0.4)
		start := g.rng.Intn(len(songs))
		platform := g.pick([]string{"android", "android", "linux", "ios", "windows"})
		shuffle := !album
		for i := 0; i < n; i++ {
			s := songs[(start+i)%len(songs)]
			if !album {
				s = songs[g.rng.Intn(len(songs))]
			}
			ms := int64(g.between(150, 290)) * 1000
			reasonEnd := "trackdone"
			if g.chance(0.18) { // skipped
				ms, reasonEnd = int64(g.between(5, 60))*1000, "fwdbtn"
			}
			at = at.Add(time.Duration(ms) * time.Millisecond)
			u := trackURI((start + i) % len(songs))
			if !album {
				u = uri("track", s.Track+s.Artist)
			}
			ev := base("spotify.play", domain.CategoryConsumption, at)
			ev.ExternalID, ev.Title = fmt.Sprintf("play:%d:%s:%d", at.Unix(), short(u)[:12], ms), s.Artist+" — "+s.Track
			ev.Metadata["export"], ev.Metadata["kind"], ev.Metadata["ms_played"], ev.Metadata["uri"] = "extended", "track", ms, u
			ev.Metadata["album"], ev.Metadata["platform"], ev.Metadata["country"] = s.Album, platform, "DE"
			ev.Metadata["reason_start"], ev.Metadata["reason_end"], ev.Metadata["ip"] = "trackdone", reasonEnd, "203.0.113.7"
			ev.Metadata["skipped"], ev.Metadata["shuffle"], ev.Metadata["offline"], ev.Metadata["incognito"] = reasonEnd == "fwdbtn", shuffle, false, false
			if i == 0 {
				ev.Metadata["reason_start"] = g.pick([]string{"clickrow", "playbtn", "appload"})
			}
			g.add(ev)
			if g.chance(0.05) { // a pause mid-session
				at = at.Add(g.minutes(3, 20))
			}
		}
	}
	// the session that ended half an hour ago, so the first timeline page
	// opens on a cluster
	session(g.now.Add(-2*time.Hour), 22)
	// dense: ~2.5 sessions a week over the last 52 weeks
	for w := 52; w >= 1; w-- {
		week := g.now.Add(-time.Duration(w) * 7 * 24 * time.Hour)
		for s := 0; s < g.between(2, 3); s++ {
			day := week.Add(-time.Duration(g.rng.Intn(7)) * 24 * time.Hour)
			if day.After(g.now) {
				continue
			}
			session(g.clock(day, 7, 22), g.between(8, 40))
		}
	}
	// sparse tail: a session every ~10 weeks from mid-2018
	for at := time.Date(2018, time.June, 3, 0, 0, 0, 0, time.UTC); at.Before(g.now.Add(-53 * 7 * 24 * time.Hour)); at = at.Add(time.Duration(g.between(55, 85)) * 24 * time.Hour) {
		session(g.clock(at, 8, 22), g.between(6, 16))
	}
	// a podcast episode now and then
	for i := 0; i < 25; i++ {
		at := g.since(2020)
		ms := int64(g.between(20, 55)) * 60 * 1000
		show := g.pick([]string{"The Slow Loaf Hour", "Kilometre Club Radio", "Query Plan Podcast", "Rack & Ruin After Dark"})
		ep := fmt.Sprintf("Episode %d: %s", g.between(10, 140), g.pick([]string{"Hydration, honestly", "Negative splits", "Indexes you forgot", "The 300€ NAS", "Listener questions", "Salt"}))
		u := uri("episode", show+ep)
		ev := base("spotify.play", domain.CategoryConsumption, at)
		ev.ExternalID, ev.Title = fmt.Sprintf("play:%d:%s:%d", at.Unix(), short(u)[:12], ms), show+" — "+ep
		ev.Metadata["export"], ev.Metadata["kind"], ev.Metadata["ms_played"], ev.Metadata["uri"], ev.Metadata["platform"], ev.Metadata["country"], ev.Metadata["reason_start"], ev.Metadata["reason_end"] = "extended", "episode", ms, u, "android", "DE", "clickrow", "endplay"
		g.add(ev)
	}

	for i := 0; i < 50; i++ {
		at := g.since(2019)
		q := g.pick(spotifySearches)
		ev := base("spotify.search", domain.CategoryConsumption, at)
		ev.ExternalID, ev.Title, ev.Body = fmt.Sprintf("search:%d:%s", at.UnixMilli(), short(q + fmt.Sprint(i))[:12]), "Searched Spotify", q
		ev.Metadata["member"], ev.Metadata["platform"] = "Spotify Account Data/SearchQueries.json", g.pick([]string{"ANDROID", "DESKTOP", "IOS"})
		if g.chance(0.5) {
			ev.Metadata["interaction_uris"] = []string{trackURI(g.rng.Intn(len(songs)))}
		}
		g.add(ev)
	}
	// library: saved tracks/albums/shows, followed artists. The real export
	// carries no dates for these; the demo dates them so the timeline shows
	// them among everything else.
	seenSave := map[string]bool{}
	for i := 0; i < 40; i++ {
		s := songs[g.rng.Intn(len(songs))]
		kind, label, u := "track", s.Artist+" — "+s.Track, uri("track", s.Track+s.Artist)
		switch i % 8 {
		case 5:
			kind, label, u = "album", s.Artist+" — "+s.Album, uri("album", s.Album+s.Artist)
		case 6:
			kind, label, u = "artist", s.Artist, uri("artist", s.Artist)
		case 7:
			show := g.pick([]string{"The Slow Loaf Hour", "Kilometre Club Radio", "Query Plan Podcast"})
			kind, label, u = "show", "Example Media — "+show, uri("show", show)
		}
		if seenSave[u] {
			continue
		}
		seenSave[u] = true
		ev := base("spotify.save", domain.CategorySave, g.since(2019))
		ev.ExternalID, ev.Title = "save:"+kind+":"+u, "Saved "+kind+": "+label
		if kind == "artist" {
			ev.Title = "Followed artist: " + label
		}
		ev.Targets = []domain.Target{{Relation: domain.RelationSaved, TargetSource: src, TargetExternalID: u, TargetURL: open(u)}}
		ev.Metadata["member"], ev.Metadata["kind"], ev.Metadata["uri"] = "Spotify Account Data/YourLibrary.json", kind, u
		switch kind {
		case "track":
			ev.Metadata["artist"], ev.Metadata["album"] = s.Artist, s.Album
		case "album":
			ev.Metadata["artist"] = s.Artist
		case "show":
			ev.Metadata["publisher"] = "Example Media"
		}
		g.add(ev)
	}
	for _, name := range spotifyPlaylists {
		modified := g.since(2019)
		n := g.between(6, 14)
		ev := base("spotify.playlist", domain.CategorySave, modified)
		ev.ExternalID, ev.Title, ev.Body = "playlist:"+short(name)[:12], "Playlist: "+name, g.pick([]string{"", "For the drive", "No lyrics, no distractions", "Sunday, slowly", "180 steps per minute or bust", "Kitchen disco 🪩"})
		if ev.Body == "" {
			ev.Body = "Made by " + self.Name
		}
		ev.Metadata["member"], ev.Metadata["playlist"], ev.Metadata["items"], ev.Metadata["followers"], ev.Metadata["last_modified"] = "Spotify Account Data/Playlist1.json", name, n, int64(g.between(0, 12)), modified.Format("2006-01-02")
		g.add(ev)
		seen := map[int]bool{}
		for i := 0; i < n; i++ {
			k := g.rng.Intn(len(songs))
			if seen[k] {
				continue
			}
			seen[k] = true
			s := songs[k]
			added := g.uniform(modified.Add(-400*24*time.Hour), modified)
			u := trackURI(k)
			item := base("spotify.playlist_item", domain.CategorySave, added)
			item.ExternalID, item.Title = fmt.Sprintf("playlist_item:%s:%s:%s", short(name)[:12], short(u)[:12], added.Format("2006-01-02")), "Added "+s.Artist+" — "+s.Track+" to "+name
			item.ThreadExternalID = ev.ExternalID
			item.Targets = []domain.Target{{Relation: domain.RelationSaved, TargetSource: src, TargetExternalID: u, TargetURL: open(u)}}
			item.Metadata["member"], item.Metadata["playlist"], item.Metadata["kind"], item.Metadata["album"], item.Metadata["uri"] = "Spotify Account Data/Playlist1.json", name, "track", s.Album, u
			g.add(item)
		}
	}

	g.add(domain.Event{
		SourceID: src, ExternalID: "spotify:unknown:Spotify Account Data/YourLibrary.json#bannedTracks", Type: "spotify.unknown", Category: domain.CategoryOther,
		Title:    `Unmapped export data: Spotify Account Data/YourLibrary.json ("bannedTracks", 3 records)`,
		Metadata: map[string]any{"member": "Spotify Account Data/YourLibrary.json", "key": "bannedTracks", "count": 3},
	})
	g.add(g.unknown(src, "Spotify Account Data/Userdata.json", `{"username":"janedoe","email":"jane.doe@example.com","country":"DE","createdFromFacebook":false,"creationTime":"2018-05-30"}`, 311))
	g.add(g.unknown(src, "Spotify Account Data/Inferences.json", `{"inferences":["1P_Custom_Running","1P_Custom_Coffee_Lovers","3P_Custom_Board_Games", …`, 2874))
}
