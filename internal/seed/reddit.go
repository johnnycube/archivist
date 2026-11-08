package seed

import (
	"fmt"
	"strings"
	"time"

	"github.com/johnnycube/archivist/internal/domain"
)

// reddit: a decade of posting in a few subreddits. Shapes follow
// providers/reddit (Actor "self", metadata "member" = the CSV the row came
// from, "Comment in r/<sub>" titles, votes/saves targeting posts).
func reddit(g *gen) {
	const src = "reddit"
	base := func(typ string, cat domain.Category, member string, at time.Time) domain.Event {
		return domain.Event{SourceID: src, Type: typ, Category: cat, Actor: "self", OccurredAt: at, Metadata: map[string]any{"member": member}}
	}
	permalink := func(sub, id string) string {
		return fmt.Sprintf("https://www.reddit.com/r/%s/comments/%s/", sub, strings.TrimPrefix(id, "seed-reddit-"))
	}

	// posts (and their permalinks, for comments/votes/saves to point at)
	type post struct{ id, sub, perma string }
	var posts []post
	for i := 0; i < 90; i++ {
		sub := g.pick(subreddits)
		title := g.pick(redditPostTitles)
		switch {
		case strings.Contains(title, "%s"):
			title = fmt.Sprintf(title, sub)
		case strings.Contains(title, "%d"):
			title = fmt.Sprintf(title, 2024+g.rng.Intn(3))
		}
		id := g.id(src, "post")
		p := post{id, sub, permalink(sub, id)}
		posts = append(posts, p)
		ev := base("reddit.post", domain.CategoryContent, "posts.csv", g.since(2016))
		ev.ExternalID, ev.Title, ev.Body, ev.ThreadExternalID = id, title, g.pick(redditBodies), id
		ev.Metadata["subreddit"], ev.Metadata["permalink"], ev.Metadata["gildings"] = sub, p.perma, "0"
		if g.chance(0.25) { // link post with a comment from the poster
			s := sites[g.rng.Intn(len(sites))]
			ev.Metadata["url"], ev.Body = s.URL, "Sharing this because it finally explained it for me: "+s.Title
		}
		g.add(ev)
	}
	for i := 0; i < 3; i++ {
		ev := base("reddit.draft", domain.CategoryContent, "drafts.csv", g.since(2023))
		ev.ExternalID, ev.Title, ev.Body = g.id(src, "draft"), g.pick(redditPostTitles[8:]), "(draft) "+g.pick(redditBodies)
		ev.Metadata["subreddit"] = g.pick(subreddits)
		g.add(ev)
	}
	for i := 0; i < 2; i++ {
		ev := base("reddit.scheduled_post", domain.CategoryContent, "scheduled_posts.csv", g.now.Add(time.Duration(24*(i+3))*time.Hour))
		ev.ExternalID = g.id(src, "scheduled")
		ev.Title, ev.Body, ev.ThreadExternalID = "Weekly self-hosting wins thread", "What did you get running this week? Screenshots welcome.", ev.ExternalID
		ev.Metadata["subreddit"], ev.Metadata["gildings"] = "selfhosted", "0"
		g.add(ev)
	}

	// comments: replies to own posts and to others', some nested
	var comments []string
	for i := 0; i < 260; i++ {
		p := posts[g.rng.Intn(len(posts))]
		parent, parentURL := p.id, p.perma
		if len(comments) > 0 && g.chance(0.3) {
			parent = comments[g.rng.Intn(len(comments))]
			parentURL = p.perma + strings.TrimPrefix(parent, "seed-reddit-") + "/"
		}
		if g.chance(0.5) { // a thread the persona did not start
			parent = fmt.Sprintf("post:%08x", hash64(p.id+"x")&0xffffffff)
			parentURL = permalink(p.sub, parent[5:])
		}
		id := g.id(src, "comment")
		comments = append(comments, id)
		ev := base("reddit.comment", domain.CategoryContent, "comments.csv", g.since(2016))
		ev.ExternalID, ev.Title, ev.Body = id, "Comment in r/"+p.sub, g.pick(redditComments)
		ev.ThreadExternalID, ev.ParentExternalID = p.id, parent
		ev.Targets = []domain.Target{{Relation: domain.RelationRepliedTo, TargetSource: src, TargetExternalID: parent, TargetURL: parentURL}}
		ev.Metadata["subreddit"], ev.Metadata["permalink"], ev.Metadata["gildings"] = p.sub, parentURL+strings.TrimPrefix(id, "seed-reddit-")+"/", "0"
		g.add(ev)
	}

	// votes: mostly other people's posts, sometimes own
	vote := func(n int, typ string, rel domain.Relation, member string) {
		for i := 0; i < n; i++ {
			p := posts[g.rng.Intn(len(posts))]
			target, url := p.id, p.perma
			if g.chance(0.7) {
				target = fmt.Sprintf("%07x", hash64(fmt.Sprintf("%s%d", typ, i))&0xfffffff)
				url = permalink(g.pick(subreddits), target)
			}
			ev := base(typ, domain.CategoryReaction, member, g.since(2016))
			ev.ExternalID, ev.Title = g.id(src, strings.ReplaceAll(strings.TrimPrefix(typ, "reddit."), ".", "-")), "Voted on "+url
			ev.Targets = []domain.Target{{Relation: rel, TargetSource: src, TargetExternalID: target, TargetURL: url}}
			g.add(ev)
		}
	}
	vote(220, "reddit.vote.up", domain.RelationVotedUp, "post_votes.csv")
	vote(40, "reddit.vote.down", domain.RelationVotedDown, "post_votes.csv")
	vote(10, "reddit.vote", domain.RelationLiked, "comment_votes.csv")
	for i := 0; i < 8; i++ {
		p := posts[g.rng.Intn(len(posts))]
		ev := base("reddit.poll_vote", domain.CategoryReaction, "poll_votes.csv", g.since(2020))
		ev.ExternalID, ev.Title = g.id(src, "poll"), "Voted in a poll"
		ev.Targets = []domain.Target{{Relation: domain.RelationVotedUp, TargetSource: src, TargetExternalID: p.id, TargetURL: p.perma}}
		ev.Metadata["option"] = g.pick([]string{"Yes", "No", "Option B", "Postgres", "SQLite", "Depends"})
		g.add(ev)
	}
	for i := 0; i < 45; i++ {
		p := posts[g.rng.Intn(len(posts))]
		ev := base("reddit.save", domain.CategorySave, "saved_posts.csv", g.since(2017))
		ev.ExternalID, ev.Title = g.id(src, "save"), "Saved "+p.perma
		ev.Targets = []domain.Target{{Relation: domain.RelationSaved, TargetSource: src, TargetExternalID: p.id, TargetURL: p.perma}}
		g.add(ev)
	}
	for i := 0; i < 12; i++ {
		id := fmt.Sprintf("%07x", hash64(fmt.Sprintf("hide%d", i))&0xfffffff)
		url := permalink(g.pick(subreddits), id)
		ev := base("reddit.hide", domain.CategoryOther, "hidden_posts.csv", g.since(2018))
		ev.ExternalID, ev.Title = g.id(src, "hide"), "Hid a post"
		ev.Targets = []domain.Target{{Relation: domain.RelationSaved, TargetSource: src, TargetExternalID: id, TargetURL: url}}
		g.add(ev)
	}

	// community
	for _, sub := range subreddits {
		ev := base("reddit.subscribe", domain.CategoryConnection, "subscribed_subreddits.csv", g.since(2016))
		ev.ExternalID, ev.Title = "subscribe:"+sub, "Subscribed to r/"+sub
		ev.Targets = []domain.Target{{Relation: domain.RelationFollowed, TargetSource: src, TargetExternalID: "r/" + sub, TargetURL: "https://www.reddit.com/r/" + sub}}
		g.add(ev)
	}
	for _, sub := range []string{"selfhosted", "homelab", "sourdough"} {
		ev := base("reddit.moderate", domain.CategoryConnection, "moderated_subreddits.csv", g.since(2021))
		ev.ExternalID, ev.Title = "moderate:"+sub, "Moderator/approved in r/"+sub
		ev.Targets = []domain.Target{{Relation: domain.RelationFollowed, TargetSource: src, TargetExternalID: "r/" + sub, TargetURL: "https://www.reddit.com/r/" + sub}}
		g.add(ev)
	}
	for _, p := range g.some(8) {
		ev := base("reddit.friend", domain.CategoryConnection, "friends.csv", g.since(2017))
		ev.ExternalID, ev.Title = "friend:"+p.Handle, "Friended u/"+p.Handle
		ev.Targets = []domain.Target{{Relation: domain.RelationFollowed, TargetSource: src, TargetExternalID: "u/" + p.Handle, TargetURL: "https://www.reddit.com/user/" + p.Handle}}
		g.add(ev)
	}
	for _, name := range []string{"dev", "outdoors", "kitchen", "berlin"} {
		ev := base("reddit.multireddit", domain.CategoryConnection, "multireddits.csv", g.since(2019))
		ev.ExternalID, ev.Title = "multi:"+name, "Custom feed: "+name
		g.add(ev)
	}
	for i := 0; i < 5; i++ {
		p := posts[g.rng.Intn(len(posts))]
		ev := base("reddit.gild", domain.CategoryOther, "gilded_content.csv", g.since(2019))
		ev.ExternalID, ev.Title = g.id(src, "gild"), "Awarded "+p.perma
		ev.Metadata["direction"], ev.Metadata["award"] = "given", g.pick([]string{"Gold", "Silver", "Helpful", "Wholesome"})
		if i%2 == 1 {
			ev.Type, ev.Title, ev.Actor = "reddit.gild", "Received an award on "+p.perma, "u/"+g.pick(handles)
			ev.Metadata["direction"], ev.Metadata["member"] = "received", "gold_received.csv"
		}
		ev.Targets = []domain.Target{{Relation: domain.RelationLiked, TargetSource: src, TargetExternalID: p.id, TargetURL: p.perma}}
		g.add(ev)
	}

	// private messages and chat
	subjects := []string{"Re: your post about data exports", "Question about your homelab setup", "Mod invite for r/selfhosted", "Meetup in Berlin?", "Thanks!", "Re: sourdough hydration"}
	for t := 0; t < 8; t++ {
		other := g.pick(handles)
		thread := fmt.Sprintf("th%d", t+1)
		for _, l := range g.conversation(g.since(2017), []person{{Name: other, Handle: other}}, g.between(2, 5)) {
			ev := base("reddit.message", domain.CategoryMessage, "messages.csv", l.At)
			ev.ExternalID, ev.Title, ev.Body, ev.ThreadExternalID = g.id(src, "message"), subjects[t%len(subjects)], l.Body, thread
			ev.Metadata["to"] = other
			if !l.Self {
				ev.Actor, ev.Metadata["to"] = other, strings.TrimPrefix(self.Reddit, "u/")
			}
			g.add(ev)
		}
	}
	for c := 0; c < 4; c++ {
		channel := fmt.Sprintf("sendbird_group_channel_%d", 100+c)
		people := g.some(2)
		for _, l := range g.conversation(g.since(2022), people, g.between(8, 20)) {
			ev := base("reddit.chat", domain.CategoryMessage, "chat_history.csv", l.At)
			ev.ExternalID, ev.Title, ev.Body, ev.ThreadExternalID = g.id(src, "chat"), "Chat message", l.Body, channel
			ev.Metadata["channel"] = channel
			if !l.Self {
				ev.Actor = l.Who.Handle
			}
			g.add(ev)
		}
	}

	g.add(g.unknown(src, "statistics.csv", "statistic,value\naccount_age_days,3652\ntotal_karma,18420\n", 96))
	g.add(g.unknown(src, "user_preferences.csv", "preference,value\nnsfw,false\nbeta,true\n", 1412))
	g.add(g.unknown(src, "ip_logs.csv", "date,ip\n2026-09-01 08:12:44 UTC,203.0.113.7\n", 88412))
}
