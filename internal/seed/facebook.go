package seed

import (
	"fmt"
	"strings"
	"time"

	"github.com/johnnycube/archivist/internal/domain"
)

// facebook: the oldest account — posts, comments, Messenger threads, a lot
// of reactions, friends, groups, events and the account-security log.
// Shapes follow providers/facebook (Actor "self", export sentences as titles,
// Messenger threads under "inbox/<slug>").
func facebook(g *gen) {
	const src = "facebook"
	base := func(typ string, cat domain.Category, at time.Time) domain.Event {
		return domain.Event{SourceID: src, Type: typ, Category: cat, Actor: "self", OccurredAt: at, Metadata: map[string]any{}}
	}
	first := func(p person) string { return strings.Fields(p.Name)[0] }
	possessive := func(p person) string { return first(p) + "'s" }

	// posts, some with photos
	type post struct{ id, title string }
	var posts []post
	for i := 0; i < 70; i++ {
		ev := base("facebook.post", domain.CategoryContent, g.since(2015))
		ev.ExternalID, ev.Title, ev.Body = g.id(src, "post"), "Facebook post", g.pick(fbPosts)
		if g.chance(0.15) {
			grp := g.pick(fbGroups)
			ev.Title, ev.Metadata["context"] = self.Name+" posted in "+grp+".", "group"
		}
		if g.chance(0.2) {
			ev.Metadata["link"] = sites[g.rng.Intn(len(sites))].URL
		}
		if g.chance(0.2) {
			ev.Media = []domain.Media{g.picture(src, fmt.Sprintf("your_facebook_activity/posts/media/IMG_%s.png", ev.OccurredAt.Format("20060102_150405")), styleLandscape)}
		}
		posts = append(posts, post{ev.ExternalID, ev.Title})
		g.add(ev)
	}
	for i := 0; i < 120; i++ {
		p := friends[g.rng.Intn(len(friends))]
		ev := base("facebook.comment", domain.CategoryContent, g.since(2015))
		ev.ExternalID, ev.Title, ev.Body = g.id(src, "comment"), fmt.Sprintf("%s commented on %s post.", self.Name, possessive(p)), g.pick(fbComments)
		if g.chance(0.2) {
			grp := g.pick(fbGroups)
			ev.Title, ev.Metadata["group"] = fmt.Sprintf("%s commented on a post in %s.", self.Name, grp), grp
		}
		if g.chance(0.15) {
			own := posts[g.rng.Intn(len(posts))]
			ev.Title, ev.ThreadExternalID, ev.ParentExternalID = self.Name+" replied to a comment on her own post.", own.id, own.id
		}
		g.add(ev)
	}
	for i := 0; i < 6; i++ {
		app := g.pick(fbApps)
		ev := base("facebook.app_post", domain.CategoryContent, g.since(2016))
		ev.ExternalID, ev.Title = g.id(src, "app-post"), fmt.Sprintf("%s shared a %s activity.", self.Name, app)
		ev.Metadata["app"] = app
		g.add(ev)
	}

	// Messenger: three one-to-one threads and one group
	for _, people := range [][]person{friends[:1], friends[1:2], friends[3:4], friends[:3]} {
		names := make([]string, 0, len(people)+1)
		for _, p := range people {
			names = append(names, p.Name)
		}
		names = append(names, self.Name)
		slug := strings.ToLower(strings.ReplaceAll(people[0].Name, " ", "")) + fmt.Sprintf("_%d", 1000+hash64(people[0].Handle)%9000)
		if len(people) > 1 {
			slug = "hikingcrew_" + fmt.Sprint(4242)
		}
		thread := "inbox/" + slug
		for _, l := range g.conversation(g.since(2015), people, g.between(20, 40)) {
			ev := base("facebook.message", domain.CategoryMessage, l.At)
			ev.ExternalID, ev.Title, ev.Body, ev.ThreadExternalID = g.id(src, "message"), "Message in "+strings.Join(names, ", "), l.Body, thread
			ev.Metadata["folder"] = "inbox"
			if !l.Self {
				ev.Actor = l.Who.Name
			}
			if l.Body == "Photo" {
				ev.Body = g.pick([]string{"look at this 😍", "proof!", "found it", "this one?", "the view from up here"})
				ev.Media = []domain.Media{g.picture(src, fmt.Sprintf("your_facebook_activity/messages/%s/photos/%d.png", thread, 100000+g.counts[src+"/message"]), styleGradient)}
			} else if g.chance(0.05) {
				ev.Metadata["link"] = sites[g.rng.Intn(len(sites))].URL
			}
			g.add(ev)
		}
	}

	// reactions: to friends' posts/photos, some to the persona's own seeded posts
	reactions := []string{"LIKE", "LIKE", "LIKE", "LOVE", "HAHA", "WOW", "CARE", "SAD"}
	for i := 0; i < 200; i++ {
		p := cast[g.rng.Intn(len(cast))]
		what := g.pick([]string{"post", "photo", "comment", "video", "link"})
		ev := base("facebook.reaction", domain.CategoryReaction, g.since(2015))
		ev.ExternalID, ev.Title = g.id(src, "reaction"), fmt.Sprintf("%s reacted to %s %s.", self.Name, possessive(p), what)
		ev.Metadata["reaction"] = g.pick(reactions)
		ev.Targets = []domain.Target{{Relation: domain.RelationLiked, TargetSource: src, TargetExternalID: ev.Title}}
		if g.chance(0.1) {
			own := posts[g.rng.Intn(len(posts))]
			ev.Targets[0].TargetExternalID = own.id
		}
		g.add(ev)
	}

	// people and pages
	for _, p := range cast {
		ev := base("facebook.friend", domain.CategoryConnection, g.since(2015))
		ev.ExternalID, ev.Title = "friend:"+p.Handle, "Friended "+p.Name
		ev.Targets = []domain.Target{{Relation: domain.RelationFollowed, TargetSource: src, TargetExternalID: p.Name}}
		g.add(ev)
	}
	for i := 0; i < 48; i++ {
		name := g.pick([]string{"Sam Sample", "Pat Placeholder", "Chris Doe", "Kim Example", "Lee Mustermann", "Ola Nordmann", "Jan Jansen", "Joe Bloggs", "Tommy Atkins", "Juan Pérez", "Pierre Dupont", "Ivan Ivanov"}) + fmt.Sprintf(" %d", i)
		ev := base("facebook.friend", domain.CategoryConnection, g.since(2015))
		ev.ExternalID, ev.Title = "friend:"+short(name), "Friended "+strings.TrimRight(name, " 0123456789")
		ev.Targets = []domain.Target{{Relation: domain.RelationFollowed, TargetSource: src, TargetExternalID: ev.Title[9:]}}
		g.add(ev)
	}
	for i, dir := range []string{"sent", "sent", "sent", "sent", "received", "received", "received", "received", "received", "rejected"} {
		name := g.pick([]string{"Sam Sample", "Pat Placeholder", "Chris Doe", "Kim Example", "Ola Nordmann", "Joe Bloggs"})
		ev := base("facebook.friend_request", domain.CategoryConnection, g.since(2016))
		ev.ExternalID = fmt.Sprintf("friend_request_%s:%d", dir, i)
		ev.Title = map[string]string{"sent": "Sent a friend request to ", "received": "Friend request from ", "rejected": "Rejected a friend request from "}[dir] + name
		ev.Metadata["direction"], ev.Metadata["name"] = dir, name
		g.add(ev)
	}
	pages := []string{"Slow Loaf", "Kilometre Club", "Berlin Runners", "Example Bike Shop", "Bean Counter Roastery", "Table Talk Café", "Rack & Ruin", "Gardenlog", "Analog Workshop", "Kesselhaus Berlin"}
	for i, name := range pages {
		ev := base("facebook.follow", domain.CategoryConnection, g.since(2016))
		url := "https://www.facebook.com/" + strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(name, " ", ""), "&", "and"))
		rel := domain.RelationFollowed
		ev.ExternalID, ev.Title = "follow:"+short(name), "Followed "+name
		if i%2 == 0 {
			ev.ExternalID, ev.Title, ev.Metadata["via"], rel = "page_like:"+short(name), "Liked the page "+name, "page_like", domain.RelationLiked
		}
		ev.Targets = []domain.Target{{Relation: rel, TargetSource: src, TargetExternalID: name, TargetURL: url}}
		g.add(ev)
	}
	for _, name := range []string{"Loud Uncle", "Daily Deals Aggregator", "Motivation Quotes 24/7", "Crypto Signals Now", "Sam Sample"} {
		ev := base("facebook.unfollow", domain.CategoryConnection, g.since(2018))
		ev.ExternalID, ev.Title = "unfollow:"+short(name), "Unfollowed "+name
		ev.Targets = []domain.Target{{Relation: domain.RelationFollowed, TargetSource: src, TargetExternalID: name}}
		g.add(ev)
	}
	for _, grp := range fbGroups {
		ev := base("facebook.group_join", domain.CategoryConnection, g.since(2016))
		ev.ExternalID, ev.Title = "group_join:"+short(grp), fmt.Sprintf("%s joined %s.", self.Name, grp)
		ev.Targets = []domain.Target{{Relation: domain.RelationFollowed, TargetSource: src, TargetExternalID: grp}}
		g.add(ev)
	}
	for i, name := range append(fbEvents, fbEvents[:6]...) {
		rsvp := []string{"joined", "joined", "interested", "invited", "declined"}[i%5]
		start := g.clock(g.since(2016), 10, 20)
		ev := base("facebook.event", domain.CategoryConnection, start)
		ev.ExternalID = fmt.Sprintf("event_%s:%d:%s", rsvp, start.Unix(), short(name)[:8])
		ev.Title = map[string]string{"joined": "Going to ", "interested": "Interested in ", "invited": "Invited to ", "declined": "Declined "}[rsvp] + name
		ev.Metadata["rsvp"], ev.Metadata["event"], ev.Metadata["ends_at"] = rsvp, name, rfc3339(start.Add(time.Duration(g.between(2, 8))*time.Hour))
		g.add(ev)
	}
	for i, app := range append(fbApps, "Old Quiz App", "FarmVille") {
		ev := base("facebook.app", domain.CategoryConnection, g.since(2015))
		ev.ExternalID, ev.Title = "app:"+short(app), "Connected the app "+app
		ev.Metadata["app"], ev.Metadata["status"] = app, "active"
		if i >= len(fbApps) {
			ev.Title, ev.Metadata["status"], ev.Metadata["removed_at"] = "Removed the app "+app, "removed", rfc3339(ev.OccurredAt)
		}
		g.add(ev)
	}
	for i := 0; i < 40; i++ {
		ev := base("facebook.search", domain.CategoryConsumption, g.since(2017))
		ev.ExternalID, ev.Title, ev.Body = g.id(src, "search"), "Searched Facebook", g.pick(fbSearches)
		g.add(ev)
	}

	// photo albums
	albums := []string{"Coast trip 2019", "Kernel", "Berlin Half Marathon", "Balcony garden", "Mobile uploads"}
	for i := 0; i < 40; i++ {
		album := albums[i%len(albums)]
		at := g.since(2015)
		ev := base("facebook.photo", domain.CategoryMedia, at)
		uri := fmt.Sprintf("your_facebook_activity/posts/album/%s/IMG_%s.png", strings.ToLower(strings.ReplaceAll(album, " ", "_")), at.Format("20060102_150405"))
		ev.ExternalID, ev.Title = "photo:"+short(uri), "Photo"
		if g.chance(0.4) {
			ev.Title, ev.Body = g.pick([]string{"Kernel, asleep on the NAS", "Finish line!", "First tomato", "Sunset over the Spree", "Race bib pickup", "Hut breakfast"}), g.pick([]string{"", "", "Taken on the Pixel.", "Album: " + album})
		}
		if i < 8 { // a handful with real bytes behind them
			ev.Media = []domain.Media{g.picture(src, uri, pictureStyle(i%4))}
		} else {
			ev.Media = []domain.Media{{BlobKey: uri}}
		}
		g.add(ev)
	}

	// profile and security
	for i, upd := range []string{"Updated profile picture", "Updated cover photo", "Added a new workplace", "Changed relationship status", "Updated bio", "Added a hometown", "Email change", "Password change", "Updated profile picture", "Phone number change"} {
		ev := base("facebook.profile_update", domain.CategoryProfile, g.since(2015))
		ev.ExternalID, ev.Title = fmt.Sprintf("profile_update:%d", i), upd
		if strings.HasSuffix(upd, "change") {
			ev.Metadata["action"], ev.Metadata["ip"], ev.Metadata["user_agent"] = upd, "203.0.113.7", "Mozilla/5.0 (X11; Linux x86_64) Firefox/130.0"
			if upd == "Email change" {
				ev.Metadata["new_email"], ev.Metadata["old_email"] = self.Email, "jane.doe.2015@example.com"
			}
		}
		g.add(ev)
	}
	for i := 0; i < 30; i++ {
		action := g.pick([]string{"Login", "Login", "Login", "Logout", "Session updated", "Password change", "Login approval"})
		ev := base("facebook.login", domain.CategoryOther, g.since(2015))
		ev.ExternalID, ev.Title = fmt.Sprintf("login:%d", i), action
		ev.Metadata["action"], ev.Metadata["ip"], ev.Metadata["site"] = action, fmt.Sprintf("203.0.113.%d", g.between(1, 254)), g.pick([]string{"www.facebook.com", "m.facebook.com", "Facebook for Android"})
		ev.Metadata["user_agent"], ev.Metadata["location"] = g.pick([]string{"Mozilla/5.0 (X11; Linux x86_64) Firefox/130.0", "Mozilla/5.0 (Linux; Android 14; Pixel 8) Chrome/128.0", "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_5) Safari/605.1.15"}), strings.ReplaceAll(g.pick(cities), ", ", ", Land, ")
		g.add(ev)
	}

	g.add(g.unknown(src, "security_and_login_information/browser_cookies.json", `{"datr_stats_v2": [{"datr": "…", "creation_timestamp": 1704067200}]}`, 4102))
	g.add(g.unknown(src, "preferences/language_and_locale.json", `{"language_and_locale_v2": [{"name": "Language Settings", "children": [{"name": "Locale", "entries": [{"data": {"value": "en_GB"}}]}]}]}`, 1309))
	g.add(domain.Event{
		SourceID: src, ExternalID: "facebook:unknown:your_facebook_activity/posts/your_posts__check_ins__photos_and_videos_1.json#label_values",
		Type: "facebook.unknown", Category: domain.CategoryOther,
		Title:    `Unmapped export data: your_facebook_activity/posts/your_posts__check_ins__photos_and_videos_1.json ("label_values", 12 records)`,
		Metadata: map[string]any{"member": "your_facebook_activity/posts/your_posts__check_ins__photos_and_videos_1.json", "key": "label_values", "count": 12},
	})
}
