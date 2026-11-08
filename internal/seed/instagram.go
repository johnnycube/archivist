package seed

import (
	"fmt"
	"strings"
	"time"

	"github.com/johnnycube/archivist/internal/domain"
)

// instagram: photo posts and stories, DMs, and a lot of likes. Shapes follow
// providers/instagram (Actor "self", "Liked <account>" titles, DM threads
// under "inbox/<slug>" with reactions in metadata).
func instagram(g *gen) {
	const src = "instagram"
	base := func(typ string, cat domain.Category, at time.Time) domain.Event {
		return domain.Event{SourceID: src, Type: typ, Category: cat, Actor: "self", OccurredAt: at, Metadata: map[string]any{}}
	}
	profile := func(acc string) string { return "https://www.instagram.com/" + acc }

	// posts: every one has a photo; a few are reels or archived
	type post struct{ id, url string }
	var posts []post
	for i := 0; i < 60; i++ {
		at := g.since(2017)
		uri := fmt.Sprintf("media/posts/%s/IMG_%04d.png", at.Format("200601"), 4000+i)
		ev := base("instagram.post", domain.CategoryContent, at)
		ev.ExternalID, ev.Title, ev.Body = "post:"+uri, "Instagram post", g.pick(igCaptions)
		if i%7 == 6 {
			uri = strings.Replace(strings.Replace(uri, "/posts/", "/reels/", 1), "IMG_", "REEL_", 1)
			ev.ExternalID, ev.Title, ev.Metadata["via"] = "post:"+uri, "Instagram reel", "reels"
		}
		if i%11 == 10 {
			ev.Metadata["archived"] = true
		}
		if i < 6 {
			ev.Media = []domain.Media{g.picture(src, uri, pictureStyle((i+1)%4))}
		} else {
			ev.Media = []domain.Media{{BlobKey: uri}}
		}
		posts = append(posts, post{ev.ExternalID, "https://www.instagram.com/p/" + short(uri)[:11] + "/"})
		g.add(ev)
	}
	for i := 0; i < 15; i++ {
		ev := base("instagram.thread_post", domain.CategoryContent, g.since(2023))
		ev.ExternalID, ev.Title, ev.Body = g.id(src, "thread-post"), "Threads post", g.pick(tweets)
		g.add(ev)
	}
	for i := 0; i < 40; i++ {
		at := g.since(2018)
		uri := fmt.Sprintf("media/stories/%s/%s_%d.png", at.Format("200601"), at.Format("20060102"), 100+i)
		ev := base("instagram.story", domain.CategoryMedia, at)
		ev.ExternalID, ev.Title = fmt.Sprintf("story:%d:%s", at.Unix(), short(uri)[:8]), "Instagram story"
		if g.chance(0.3) {
			ev.Body = g.pick(snapStories)
		}
		if i < 4 {
			ev.Media = []domain.Media{g.picture(src, uri, styleCity)}
		} else {
			ev.Media = []domain.Media{{BlobKey: uri}}
		}
		g.add(ev)
	}
	for i := 0; i < 90; i++ {
		owner := g.pick(igAccounts)
		ev := base("instagram.comment", domain.CategoryContent, g.since(2017))
		ev.ExternalID, ev.Title, ev.Body = g.id(src, "comment"), "Commented on "+owner+"'s post", g.pick([]string{"😍😍", "Where is this?!", "Recipe please 🙏", "Goals.", "Kernel says hi", "Stunning light", "Nice one!", "🔥🔥🔥", "Need this in my life", "Congrats!!"})
		ev.Metadata["media_owner"] = owner
		if g.chance(0.2) {
			ev.Title, ev.Metadata["via"] = "Commented on "+owner+"'s reel", "reels"
		}
		g.add(ev)
	}

	// DMs
	for _, people := range [][]person{friends[1:2], friends[3:4], cast[9:10], friends[:3]} {
		names := []string{}
		for _, p := range people {
			names = append(names, p.Name)
		}
		names = append(names, self.Name)
		thread := fmt.Sprintf("inbox/%s_%d", strings.ToLower(strings.ReplaceAll(people[0].Name, " ", "")), 1000+hash64(people[0].Handle)%9000)
		if len(people) > 1 {
			thread = "inbox/hikingcrew_7781"
		}
		for _, l := range g.conversation(g.since(2018), people, g.between(20, 40)) {
			ev := base("instagram.message", domain.CategoryMessage, l.At)
			ev.ExternalID, ev.Title, ev.Body, ev.ThreadExternalID = g.id(src, "message"), "Message in "+strings.Join(names, ", "), l.Body, thread
			ev.Metadata["folder"] = "inbox"
			if !l.Self {
				ev.Actor = l.Who.Name
			}
			switch {
			case l.Body == "Photo":
				ev.Body = g.pick([]string{"look at this 😍", "proof!", "found it", "this one?", "the view from up here"})
				ev.Media = []domain.Media{g.picture(src, fmt.Sprintf("your_instagram_activity/messages/%s/photos/%d.png", thread, 500000+g.counts[src+"/message"]), styleGradient)}
			case g.chance(0.08):
				ev.Body, ev.Metadata["link"] = g.pick([]string{"this is so you 😂", "we need to try this", "sent you a reel"}), "https://www.instagram.com/reel/"+short(ev.ExternalID)[:11]+"/"
				ev.Metadata["share_owner"] = g.pick(igAccounts)
			case g.chance(0.15):
				ev.Metadata["reactions"] = []map[string]string{{"reaction": g.pick([]string{"❤", "😂", "👍", "🔥"}), "actor": people[0].Name}}
			}
			g.add(ev)
		}
	}
	// message request from a stranger
	for _, l := range g.conversation(g.since(2024), []person{{Name: "Pat Placeholder", Handle: "pat.placeholder"}}, 2) {
		ev := base("instagram.message", domain.CategoryMessage, l.At)
		ev.ExternalID, ev.Title, ev.Body, ev.ThreadExternalID = g.id(src, "message"), "Message in Pat Placeholder, "+self.Name, "hi! love your balcony posts, can I ask what soil you use?", "message_requests/patplaceholder_5678"
		ev.Actor, ev.Metadata["folder"] = "Pat Placeholder", "message_requests"
		g.add(ev)
	}

	// likes and saves
	for i := 0; i < 220; i++ {
		acc := g.pick(igAccounts)
		href := "https://www.instagram.com/p/" + short(fmt.Sprint("like", i))[:11] + "/"
		ev := base("instagram.like", domain.CategoryReaction, g.since(2017))
		ev.ExternalID, ev.Title = "like:"+href, "Liked "+acc
		ev.Targets = []domain.Target{{Relation: domain.RelationLiked, TargetSource: src, TargetExternalID: acc, TargetURL: href}}
		switch {
		case i%10 == 9:
			ev.ExternalID, ev.Title, ev.Metadata["via"] = fmt.Sprintf("comment_like:%d:%s", ev.OccurredAt.Unix(), short(href)[:8]), "Liked a comment by "+acc, "comment"
		case i%10 == 4:
			ev.ExternalID, ev.Title, ev.Metadata["via"] = fmt.Sprintf("story_like:%d:%s", ev.OccurredAt.Unix(), short(href)[:8]), "Liked a story by "+acc, "story"
		}
		g.add(ev)
	}
	for i := 0; i < 40; i++ {
		acc := g.pick(igAccounts)
		href := "https://www.instagram.com/p/" + short(fmt.Sprint("save", i))[:11] + "/"
		ev := base("instagram.save", domain.CategorySave, g.since(2018))
		ev.ExternalID, ev.Title = "save:"+href, "Saved "+acc
		ev.Targets = []domain.Target{{Relation: domain.RelationSaved, TargetSource: src, TargetExternalID: acc, TargetURL: href}}
		g.add(ev)
	}

	// graph
	accounts := append([]string{}, igAccounts...)
	for _, p := range cast {
		accounts = append(accounts, strings.ReplaceAll(p.Handle, "_", "."))
	}
	for i := 0; i < 60; i++ {
		acc := accounts[i%len(accounts)]
		if i >= len(accounts) {
			acc = fmt.Sprintf("%s.%d", acc, i)
		}
		ev := base("instagram.follow", domain.CategoryConnection, g.since(2017))
		ev.ExternalID, ev.Title = "follow:"+profile(acc), "Followed "+acc
		ev.Targets = []domain.Target{{Relation: domain.RelationFollowed, TargetSource: src, TargetExternalID: acc, TargetURL: profile(acc)}}
		ev.Metadata["profile"] = profile(acc)
		if i < 3 {
			ev.ExternalID, ev.Title, ev.Metadata["list"] = "close_friend:"+acc, "Added "+acc+" to close friends", "close_friends"
		}
		g.add(ev)
	}
	for i := 0; i < 50; i++ {
		acc := accounts[(i*7)%len(accounts)]
		if i >= len(accounts) {
			acc = fmt.Sprintf("%s_%d", acc, i)
		}
		ev := base("instagram.follower", domain.CategoryConnection, g.since(2017))
		ev.ExternalID, ev.Title, ev.Actor = fmt.Sprintf("follower:%d:%s", ev.OccurredAt.Unix(), short(acc)[:8]), acc+" followed you", acc
		ev.Metadata["profile"] = profile(acc)
		g.add(ev)
	}
	for i := 0; i < 8; i++ {
		acc := g.pick([]string{"private.account", "pat.placeholder", "sam.sample", "kim.example"})
		ev := base("instagram.follow_request", domain.CategoryConnection, g.since(2019))
		if i%2 == 0 {
			ev.ExternalID, ev.Title = fmt.Sprintf("follow_request:%s:%d", acc, i), "Requested to follow "+acc
			ev.Metadata["direction"] = "sent"
			ev.Targets = []domain.Target{{Relation: domain.RelationFollowed, TargetSource: src, TargetExternalID: acc, TargetURL: profile(acc)}}
		} else {
			ev.ExternalID, ev.Title, ev.Actor = fmt.Sprintf("follow_request_received:%d:%s", ev.OccurredAt.Unix(), short(acc)[:8]), acc+" requested to follow you", acc
			ev.Metadata["direction"] = "received"
		}
		ev.Metadata["profile"] = profile(acc)
		g.add(ev)
	}
	for i := 0; i < 10; i++ {
		acc := g.pick([]string{"daily.deals.now", "motivation.quotes.247", "crypto.signals", "sam.sample", "loud.uncle"})
		ev := base("instagram.unfollow", domain.CategoryConnection, g.since(2019))
		kind := "unfollow"
		if i%4 == 3 {
			kind = "block"
		}
		ev.ExternalID = fmt.Sprintf("%s:%d:%s", kind, ev.OccurredAt.Unix(), short(acc)[:8])
		ev.Title = map[string]string{"unfollow": "Unfollowed ", "block": "Blocked "}[kind] + acc
		ev.Metadata["status"], ev.Metadata["profile"] = kind+"ed", profile(acc)
		ev.Targets = []domain.Target{{Relation: domain.RelationFollowed, TargetSource: src, TargetExternalID: acc, TargetURL: profile(acc)}}
		g.add(ev)
	}
	for i := 0; i < 40; i++ {
		ev := base("instagram.search", domain.CategoryConsumption, g.since(2019))
		ev.ExternalID, ev.Title = g.id(src, "search"), "Searched Instagram"
		if i%3 == 0 {
			ev.Body, ev.Metadata["kind"] = g.pick([]string{"sourdough", "berlin run", "homelab", "balcony garden", "kyoto autumn", "film photography", "brass birmingham"}), "word_or_phrase_searches"
		} else {
			ev.Body, ev.Metadata["kind"] = g.pick(accounts), "profile_searches"
		}
		g.add(ev)
	}
	for i := 0; i < 25; i++ {
		ev := base("instagram.login", domain.CategoryOther, g.since(2017))
		kind := "login"
		if i%5 == 4 {
			kind = "logout"
		}
		ev.ExternalID = fmt.Sprintf("%s:%d:%d", kind, ev.OccurredAt.Unix(), i)
		ev.Title = map[string]string{"login": "Logged in to Instagram", "logout": "Logged out of Instagram"}[kind]
		ev.Metadata["action"], ev.Metadata["ip"], ev.Metadata["language"], ev.Metadata["port"] = kind, fmt.Sprintf("203.0.113.%d", g.between(1, 254)), "en", fmt.Sprint(g.between(40000, 65000))
		ev.Metadata["user_agent"] = g.pick([]string{"Instagram 340.0.0.22.92 Android (34/14; Pixel 8)", "Instagram 340.0 (iPhone14,5; iOS 17_5)", "Mozilla/5.0 (X11; Linux x86_64) Firefox/130.0"})
		g.add(ev)
	}

	g.add(g.unknown(src, "personal_information/personal_information.json", `{"profile_user": [{"string_map_data": {"Username": {"value": "jane.doe.pics"}, "Name": {"value": "Jane Doe"}}}]}`, 1877))
	g.add(g.unknown(src, "ads_information/ads_and_topics/ads_viewed.json", `{"impressions_history_ads_seen": [{"string_map_data": {"Author": {"value": "example_shop"}, "Time": {"timestamp": 1719837621}}}, …`, 2214009))
	g.add(domain.Event{
		SourceID: src, ExternalID: "instagram:unknown:your_instagram_activity/content/posts_1.json#cross_post_source",
		Type: "instagram.unknown", Category: domain.CategoryOther,
		Title:    `Unmapped export data: your_instagram_activity/content/posts_1.json ("cross_post_source", 7 records)`,
		Metadata: map[string]any{"member": "your_instagram_activity/content/posts_1.json", "key": "cross_post_source", "count": 7},
	})
}
