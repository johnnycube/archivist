package seed

import (
	"fmt"
	"time"

	"github.com/johnnycube/archivist/internal/domain"
)

// tiktok: a late adopter — a handful of own videos, a lot of likes, watch
// history in evening bursts (short, so it clusters without dominating), DMs
// with two friends. Shapes follow providers/tiktok ("<kind>:<unix>:<hash>"
// ids, Actor "self", constant titles).
func tiktok(g *gen) {
	const src = "tiktok"
	base := func(typ string, cat domain.Category, at time.Time) domain.Event {
		return domain.Event{SourceID: src, Type: typ, Category: cat, Actor: "self", OccurredAt: at, Metadata: map[string]any{}}
	}
	id := func(kind string, at time.Time, disc string) string {
		return fmt.Sprintf("%s:%d:%s", kind, at.Unix(), short(disc)[:8])
	}
	video := func(seed string) (link, vid string) {
		vid = fmt.Sprintf("73%016d", hash64(seed)%1e16)
		return "https://www.tiktokv.com/share/video/" + vid + "/", vid
	}
	profile := func(u string) string { return "https://www.tiktok.com/@" + u }

	for i, cap := range tiktokCaptions {
		at := g.since(2021)
		link, vid := video(fmt.Sprint("post", i))
		ev := base("tiktok.post", domain.CategoryContent, at)
		ev.ExternalID, ev.Title = id("post", at, link), cap
		ev.Media = []domain.Media{{URL: link}}
		ev.Metadata["link"], ev.Metadata["video_id"], ev.Metadata["likes"], ev.Metadata["sound"] = link, vid, fmt.Sprint(g.between(3, 4200)), g.pick([]string{"original sound", "Neon Harbour - The Paper Lanterns", "Motorik - Autobahn Kinder", "lofi focus beats"})
		g.add(ev)
	}
	for i := 0; i < 40; i++ {
		at := g.since(2021)
		body := g.pick([]string{"Great tip, thanks!", "the cat 😭", "where is this trail?", "recipe??", "this is the one", "🔥", "tutorial please", "same energy", "how many hours a day do you practice?", "Berlin represent"})
		ev := base("tiktok.comment", domain.CategoryContent, at)
		ev.ExternalID, ev.Title, ev.Body = id("comment", at, body+fmt.Sprint(i)), "Comment", body
		g.add(ev)
	}
	for _, p := range friends[1:3] {
		for i, l := range g.conversation(g.since(2022), []person{p}, g.between(20, 35)) {
			ev := base("tiktok.message", domain.CategoryMessage, l.At)
			ev.ExternalID, ev.Title, ev.Body, ev.ThreadExternalID = id("message", l.At, p.Handle+l.Body+fmt.Sprint(i)), "Message in chat with "+p.Handle, l.Body, p.Handle
			if !l.Self {
				ev.Actor = p.Handle
			} else {
				ev.Actor = self.TikTok[1:]
			}
			if g.chance(0.15) {
				link, _ := video(fmt.Sprint("share", p.Handle, i))
				ev.Body, ev.Metadata["link"] = link, link
			}
			g.add(ev)
		}
	}
	for i := 0; i < 150; i++ {
		at := g.since(2021)
		link, vid := video(fmt.Sprint("like", i))
		ev := base("tiktok.like", domain.CategoryReaction, at)
		ev.ExternalID, ev.Title = id("like", at, link), "Liked a TikTok video"
		ev.Targets = []domain.Target{{Relation: domain.RelationLiked, TargetSource: src, TargetExternalID: vid, TargetURL: link}}
		ev.Metadata["link"] = link
		g.add(ev)
	}
	for i := 0; i < 30; i++ {
		at := g.since(2021)
		link, vid := video(fmt.Sprint("fav", i))
		ev := base("tiktok.save", domain.CategorySave, at)
		ev.ExternalID, ev.Title, ev.Metadata["kind"], ev.Metadata["link"] = id("save", at, link), "Added a TikTok video to favorites", "video", link
		ev.Targets = []domain.Target{{Relation: domain.RelationSaved, TargetSource: src, TargetExternalID: vid, TargetURL: link}}
		switch i % 10 {
		case 8:
			tag := g.pick([]string{"#homelab", "#sourdough", "#runtok", "#berlin"})
			ev.ExternalID, ev.Title, ev.Metadata["kind"], ev.Metadata["hashtag"] = id("save", at, tag), "Added "+tag+" to favorite hashtags", "hashtag", tag
			delete(ev.Metadata, "link")
			ev.Targets = []domain.Target{{Relation: domain.RelationSaved, TargetSource: src, TargetExternalID: tag}}
		case 9:
			ev.Title, ev.Metadata["kind"] = "Added a TikTok sound to favorites", "sound"
		}
		g.add(ev)
	}
	for i := 0; i < 15; i++ {
		at := g.since(2022)
		link, vid := video(fmt.Sprint("shr", i))
		method := g.pick([]string{"WhatsApp", "Copy link", "Telegram", "Instagram"})
		ev := base("tiktok.share", domain.CategoryContent, at)
		ev.ExternalID, ev.Title = id("share", at, link+method), "Shared a TikTok video via "+method
		ev.Targets = []domain.Target{{Relation: domain.RelationShared, TargetSource: src, TargetExternalID: vid, TargetURL: link}}
		ev.Metadata["shared_content"], ev.Metadata["method"], ev.Metadata["link"] = "Video", method, link
		g.add(ev)
	}
	for i := 0; i < 30; i++ {
		u := tiktokCreators[i%len(tiktokCreators)]
		if i >= len(tiktokCreators) {
			u = fmt.Sprintf("%s.%d", u, i)
		}
		at := g.since(2021)
		ev := base("tiktok.follow", domain.CategoryConnection, at)
		ev.ExternalID, ev.Title = id("follow", at, u), "Followed "+u
		ev.Targets = []domain.Target{{Relation: domain.RelationFollowed, TargetSource: src, TargetExternalID: u, TargetURL: profile(u)}}
		g.add(ev)
	}
	for i := 0; i < 25; i++ {
		u := g.pick([]string{"max_mustermann", "erika.m", "jqpublic", "sam.sample", "pat.placeholder", "kim_example", "ola.nordmann"}) + fmt.Sprintf("%02d", i)
		at := g.since(2021)
		ev := base("tiktok.follower", domain.CategoryConnection, at)
		ev.ExternalID, ev.Title, ev.Actor, ev.Metadata["username"] = id("follower", at, u), "Followed by "+u, u, u
		g.add(ev)
	}
	// watch history: evening bursts, dense enough to cluster on the timeline
	for b := 0; b < 45; b++ {
		at := g.clock(g.since(2022), 20, 23)
		for i := 0; i < g.between(4, 10); i++ {
			link, vid := video(fmt.Sprintf("watch%d-%d", b, i))
			ev := base("tiktok.watch", domain.CategoryConsumption, at)
			ev.ExternalID, ev.Title = id("watch", at, link), "Watched a TikTok video"
			ev.Targets = []domain.Target{{TargetSource: src, TargetExternalID: vid, TargetURL: link}}
			ev.Metadata["link"] = link
			g.add(ev)
			at = at.Add(time.Duration(g.between(15, 90)) * time.Second)
		}
	}
	for i := 0; i < 30; i++ {
		at := g.since(2021)
		q := g.pick(tiktokSearches)
		ev := base("tiktok.search", domain.CategoryConsumption, at)
		ev.ExternalID, ev.Title, ev.Body = id("search", at, q+fmt.Sprint(i)), "Searched TikTok", q
		g.add(ev)
	}
	for i, u := range []string{"spam_account_1", "crypto.signals.now", "free_followers_4u", "loud.uncle.tok"} {
		at := g.since(2022)
		ev := base("tiktok.block", domain.CategoryConnection, at)
		ev.ExternalID, ev.Title, ev.Metadata["username"] = id("block", at, u+fmt.Sprint(i)), "Blocked "+u, u
		ev.Targets = []domain.Target{{Relation: domain.RelationFollowed, TargetSource: src, TargetExternalID: u, TargetURL: profile(u)}}
		g.add(ev)
	}
	for i := 0; i < 15; i++ {
		at := g.since(2021)
		ev := base("tiktok.login", domain.CategoryOther, at)
		ip := fmt.Sprintf("203.0.113.%d", g.between(1, 254))
		ev.ExternalID, ev.Title = id("login", at, ip+fmt.Sprint(i)), "Logged in to TikTok"
		ev.Metadata["ip"], ev.Metadata["device_model"], ev.Metadata["device_system"], ev.Metadata["network_type"], ev.Metadata["carrier"] = ip, "Pixel 8", "Android 14", g.pick([]string{"Wi-Fi", "4G", "5G"}), "Example Mobile"
		g.add(ev)
	}

	for _, u := range []struct{ section, preview string }{
		{"Profile / Profile Information / ProfileMap", `{"userName":"janedoe.tok","bioDescription":"archives, bread, running","likesReceived":"4211","profilePhoto":"https://p16-sign.example/..."}`},
		{"App Settings / Settings / SettingsMap", `{"Allow Download":"Friends","App Language":"en","Comment Filters":"Everyone","Private Account":"Disable"}`},
		{"Tiktok Live / Watch Live History / WatchLiveMap", `{"Date":"2026-08-30 21:14:02","Link":"https://www.tiktok.com/@kernel.the.cat/live","Comments":[],"Questions":[]}`},
	} {
		ev := base("tiktok.unknown", domain.CategoryOther, time.Time{})
		ev.ExternalID, ev.Title = "unknown:"+short(u.section)[:16], "Unmapped TikTok data: "+u.section
		ev.Metadata["section"], ev.Metadata["preview"], ev.Metadata["bytes"], ev.Metadata["member"] = u.section, u.preview, len(u.preview)*3, "user_data_tiktok.json"
		g.add(ev)
	}
}
