package seed

import (
	"fmt"
	"strings"
	"time"

	"github.com/johnnycube/archivist/internal/domain"
)

// twitter: tweets since 2015, DMs with a few people, likes and follows, plus
// the undated account-level records a real export carries (blocks, mutes,
// lists). Shapes follow providers/twitter.
func twitter(g *gen) {
	const src = "twitter"
	const selfID = "1000000001"
	userID := func(p person) string { return fmt.Sprintf("10000%05d", hash64(p.Handle)%100000) }
	base := func(typ string, cat domain.Category, member string, at time.Time) domain.Event {
		return domain.Event{SourceID: src, Type: typ, Category: cat, Actor: "self", OccurredAt: at, Metadata: map[string]any{"member": member}}
	}
	sourceApps := []string{"Twitter for Android", "Twitter Web App", "Twitter for iPhone", "TweetDeck"}

	// tweets
	type tweet struct{ id, url string }
	var mine []tweet
	for i := 0; i < 220; i++ {
		id := g.id(src, "tweet")
		num := fmt.Sprintf("17%016d", g.counts[src+"/tweet"])
		t := tweet{id, "https://twitter.com/" + self.Twitter[1:] + "/status/" + num}
		mine = append(mine, t)
		ev := base("twitter.tweet", domain.CategoryContent, "data/tweets.js", g.since(2015))
		ev.ExternalID, ev.Title, ev.Body, ev.ThreadExternalID = id, "Tweet", g.pick(tweets), id
		ev.Metadata["lang"], ev.Metadata["source"] = "en", g.pick(sourceApps)
		ev.Metadata["favorite_count"], ev.Metadata["retweet_count"] = fmt.Sprint(g.rng.Intn(120)), fmt.Sprint(g.rng.Intn(20))
		switch {
		case g.chance(0.15): // reply
			h := g.pick(handles)
			parent := fmt.Sprintf("tweet:16%016d", hash64(h+fmt.Sprint(i))%1e16)
			ev.Title, ev.Body = "Reply to @"+h, "@"+h+" "+g.pick([]string{"exactly this.", "have you tried the export route instead?", "same here, took me a weekend.", "thanks, that fixed it!", "hard agree."})
			ev.ThreadExternalID, ev.ParentExternalID = parent, parent
			ev.Metadata["in_reply_to_screen_name"], ev.Metadata["in_reply_to_user_id"], ev.Metadata["mentions"] = h, fmt.Sprintf("2%09d", hash64(h)%1e9), []string{h}
			ev.Targets = []domain.Target{{Relation: domain.RelationRepliedTo, TargetSource: src, TargetExternalID: parent, TargetURL: "https://twitter.com/" + h + "/status/" + parent[6:]}}
		case g.chance(0.12): // retweet
			h := g.pick(handles)
			txt := g.pick(tweets)
			ev.Title, ev.Body = "Retweet of @"+h, "RT @"+h+": "+txt
			ev.Metadata["retweet"], ev.Metadata["retweeted_user"], ev.Metadata["retweeted_text"] = true, h, txt
		default:
			if strings.Contains(ev.Body, "#") {
				ev.Metadata["hashtags"] = []string{"selfhosted"}
			}
			if g.chance(0.1) {
				ev.Media = []domain.Media{g.picture(src, fmt.Sprintf("data/tweets_media/%s-%s.png", num, short(id)[:8]), pictureStyle(i%4))}
			}
		}
		g.add(ev)
	}
	for i := 0; i < 6; i++ {
		ev := base("twitter.note_tweet", domain.CategoryContent, "data/note-tweet.js", g.since(2023))
		ev.ExternalID, ev.Title = g.id(src, "note"), "Long-form tweet text"
		ev.Body = g.pick(redditBodies) + "\n\n" + g.pick(redditBodies)
		ev.ThreadExternalID = ev.ExternalID
		ev.Metadata["updated_at"] = rfc3339(ev.OccurredAt.Add(4 * time.Minute))
		g.add(ev)
	}
	for _, m := range []struct{ t, d string }{{"Conference day", "Talks I liked at GoDays"}, {"Marathon training", "Twelve weeks in tweets"}} {
		ev := base("twitter.moment", domain.CategoryContent, "data/moment.js", g.since(2019))
		ev.ExternalID, ev.Title, ev.Body, ev.ThreadExternalID = g.id(src, "moment"), "Moment: "+m.t, m.d, ""
		ev.ThreadExternalID = ev.ExternalID
		ev.Metadata["created_by"] = selfID
		g.add(ev)
	}

	// direct messages: five conversations, one of them a group
	for c, people := range [][]person{friends[:1], friends[1:2], friends[2:3], friends[3:4], friends[:3]} {
		group := len(people) > 1
		conv := selfID + "-" + userID(people[0])
		member, title := "data/direct-messages.js", "Direct message"
		if group {
			conv, member, title = fmt.Sprintf("15%016d", c), "data/direct-messages-group.js", "Group direct message"
			ev := base("twitter.dm_event", domain.CategoryOther, member, g.since(2019))
			ev.ExternalID, ev.Title, ev.ThreadExternalID = g.id(src, "dm-event"), "Participants joined the conversation", "dm_conversation:"+conv
			ev.Metadata["conversation_id"], ev.Metadata["group"], ev.Metadata["event"] = conv, true, "participantsJoin"
			ev.Metadata["initiating_user_id"], ev.Metadata["user_ids"] = selfID, []string{userID(people[0]), userID(people[1]), userID(people[2])}
			g.add(ev)
			ev2 := base("twitter.dm_event", domain.CategoryOther, member, ev.OccurredAt.Add(2*time.Minute))
			ev2.ExternalID, ev2.Title, ev2.ThreadExternalID = g.id(src, "dm-event"), `Conversation renamed to "Hiking crew"`, "dm_conversation:"+conv
			ev2.Metadata["conversation_id"], ev2.Metadata["group"], ev2.Metadata["event"], ev2.Metadata["name"] = conv, true, "conversationNameUpdate", "Hiking crew"
			g.add(ev2)
		}
		for _, l := range g.conversation(g.since(2018), people, g.between(10, 24)) {
			ev := base("twitter.dm", domain.CategoryMessage, member, l.At)
			ev.ExternalID, ev.Title, ev.Body, ev.ThreadExternalID = g.id(src, "dm"), title, l.Body, "dm_conversation:"+conv
			ev.Metadata["conversation_id"], ev.Metadata["sender_id"] = conv, selfID
			if !l.Self {
				ev.Actor, ev.Metadata["sender_id"] = userID(l.Who), userID(l.Who)
			}
			if !group {
				ev.Metadata["recipient_id"] = userID(people[0])
				if !l.Self {
					ev.Metadata["recipient_id"] = selfID
				}
			} else {
				ev.Metadata["group"] = true
			}
			if l.Body == "Photo" {
				ev.Media = []domain.Media{g.picture(src, fmt.Sprintf("data/direct_messages_media/%s-%s.png", ev.ExternalID[len("seed-twitter-dm-"):], short(ev.ExternalID)[:8]), styleLandscape)}
				ev.Body = g.pick([]string{"look at this 😍", "proof!", "the view from up here"})
			}
			g.add(ev)
		}
	}

	// likes: some of the persona's circle, some strangers
	for i := 0; i < 240; i++ {
		h := g.pick(handles)
		num := fmt.Sprintf("16%016d", hash64(fmt.Sprintf("like%d", i))%1e16)
		ev := base("twitter.like", domain.CategoryReaction, "data/like.js", g.since(2016))
		ev.ExternalID, ev.Title, ev.Body = "like:"+num, "Liked a tweet", g.pick(tweets)
		ev.Targets = []domain.Target{{Relation: domain.RelationLiked, TargetSource: src, TargetExternalID: "tweet:" + num, TargetURL: "https://twitter.com/" + h + "/status/" + num}}
		g.add(ev)
	}
	for i := 0; i < 60; i++ {
		id := fmt.Sprintf("2%09d", hash64(fmt.Sprintf("follow%d", i))%1e9)
		ev := base("twitter.follow", domain.CategoryConnection, "data/following.js", g.since(2015))
		ev.ExternalID, ev.Title = "follow:"+id, "Followed an account"
		ev.Targets = []domain.Target{{Relation: domain.RelationFollowed, TargetSource: src, TargetExternalID: id, TargetURL: "https://twitter.com/intent/user?user_id=" + id}}
		ev.Metadata["account_id"] = id
		g.add(ev)
	}
	for i := 0; i < 50; i++ {
		id := fmt.Sprintf("3%09d", hash64(fmt.Sprintf("follower%d", i))%1e9)
		ev := base("twitter.follower", domain.CategoryConnection, "data/follower.js", g.since(2015))
		ev.ExternalID, ev.Title, ev.Actor = "follower:"+id, "Gained a follower", id
		ev.Metadata["account_id"], ev.Metadata["user_link"] = id, "https://twitter.com/intent/user?user_id="+id
		g.add(ev)
	}
	// blocks and mutes are undated in the export: they land in the timeline's undated tail
	for i, kind := range []string{"block", "block", "block", "block", "block", "mute", "mute", "mute"} {
		id := fmt.Sprintf("4%09d", hash64(fmt.Sprintf("%s%d", kind, i))%1e9)
		ev := base("twitter."+kind, domain.CategoryConnection, "data/"+kind+".js", time.Time{})
		ev.ExternalID, ev.Title = kind+":"+id, map[string]string{"block": "Blocked an account", "mute": "Muted an account"}[kind]
		ev.Targets = []domain.Target{{Relation: domain.RelationFollowed, TargetSource: src, TargetExternalID: id, TargetURL: "https://twitter.com/intent/user?user_id=" + id}}
		ev.Metadata["account_id"] = id
		g.add(ev)
	}
	for _, l := range []struct{ rel, name, owner string }{{"created", "gophers", self.Twitter[1:]}, {"created", "berlin-runners", self.Twitter[1:]}, {"member", "svelte-people", "sveltewizard"}, {"subscribed", "homelab", "selfhost_sam"}, {"subscribed", "coffee-nerds", "roastery_notes"}} {
		url := fmt.Sprintf("https://twitter.com/%s/lists/%s", l.owner, l.name)
		ev := base("twitter.list", domain.CategoryConnection, "data/lists-"+l.rel+".js", g.since(2017))
		ev.ExternalID = "list:" + l.rel + ":" + url
		ev.Title = map[string]string{"created": "Created list ", "member": "Added to list ", "subscribed": "Subscribed to list "}[l.rel] + l.name
		ev.Targets = []domain.Target{{Relation: domain.RelationFollowed, TargetSource: src, TargetExternalID: url, TargetURL: url}}
		ev.Metadata["relation"], ev.Metadata["url"], ev.Metadata["name"] = l.rel, url, l.name
		g.add(ev)
	}
	for _, q := range []string{"golang -filter:retweets", "#selfhosted", "berlin marathon", "sourdough crumb", "svelte 5", "from:gopher_dev"} {
		ev := base("twitter.saved_search", domain.CategoryConsumption, "data/saved-search.js", g.since(2018))
		ev.ExternalID, ev.Title, ev.Body = "saved_search:"+short(q), "Saved search", q
		g.add(ev)
	}
	// account: created, profile, verification
	acc := base("twitter.account", domain.CategoryProfile, "data/account.js", time.Date(2015, time.March, 14, 9, 26, 53, 0, time.UTC))
	acc.ExternalID, acc.Title = "account:"+selfID, "Account "+self.Twitter
	acc.Metadata["account_id"], acc.Metadata["username"], acc.Metadata["display_name"], acc.Metadata["created_via"], acc.Metadata["email"] = selfID, self.Twitter[1:], self.Name, "web", self.Email
	g.add(acc)
	prof := base("twitter.account", domain.CategoryProfile, "data/profile.js", g.since(2024))
	prof.ExternalID, prof.Title, prof.Body = "profile:self", "Profile", "Builds archives. Runs slowly. Bakes bread. Opinions are my own and my cat's."
	prof.Metadata["website"], prof.Metadata["location"] = "https://janedoe.example.com", "Berlin"
	prof.Media = []domain.Media{g.picture(src, "data/profile_media/avatar.png", styleBlocks), g.picture(src, "data/profile_media/header.png", styleCity)}
	g.add(prof)
	ver := base("twitter.account", domain.CategoryProfile, "data/verified.js", g.since(2024))
	ver.ExternalID, ver.Title = "verified:"+selfID, "Verification status"
	ver.Metadata["verified"], ver.Metadata["account_id"] = false, selfID
	g.add(ver)

	g.add(g.unknown(src, "data/tweetdeck.js", `window.YTD.tweetdeck.part0 = [ { "deck": { "title": "Home", "columns": [ "Home", "Mentions" ] } } ]`, 2104))
	g.add(g.unknown(src, "data/ad-impressions.js", `window.YTD.ad_impressions.part0 = [ { "ad": { "adsUserData": { "adImpressions": { "impressions": [ ...`, 5811230))
	g.add(g.unknown(src, "data/manifest.js", `window.__THAR_CONFIG = { "userInfo": { "accountId": "1000000001", "userName": "janedoe" }, "archiveInfo": { "sizeBytes": "812345678" } }`, 9221))
}
