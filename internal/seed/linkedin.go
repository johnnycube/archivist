package seed

import (
	"fmt"
	"strings"
	"time"

	"github.com/johnnycube/archivist/internal/domain"
)

// linkedin: a career's worth of connections, a few posts, recruiter
// messages, and the profile's positions and education. Shapes follow
// providers/linkedin (Actor "self", metadata "member" = the CSV).
func linkedin(g *gen) {
	const src = "linkedin"
	base := func(typ string, cat domain.Category, member string, at time.Time) domain.Event {
		return domain.Event{SourceID: src, Type: typ, Category: cat, Actor: "self", OccurredAt: at, Metadata: map[string]any{"member": member}}
	}
	profileURL := func(p person) string {
		return "https://www.linkedin.com/in/" + strings.ReplaceAll(strings.ReplaceAll(p.Handle, "_", "-"), ".", "-") + "-example"
	}
	activity := func(seed string) (urn, link string) {
		n := fmt.Sprintf("70000000000%08d", hash64(seed)%1e8)
		return "urn:li:activity:" + n, "https://www.linkedin.com/feed/update/urn:li:activity:" + n
	}

	var shares []string
	for i, text := range linkedinPosts {
		at := g.since(2016)
		link := fmt.Sprintf("https://www.linkedin.com/feed/update/urn%%3Ali%%3Ashare%%3A70000000000%08d", 10000000+i)
		shares = append(shares, link)
		ev := base("linkedin.post", domain.CategoryContent, "Shares.csv", at)
		ev.ExternalID, ev.Title, ev.Body, ev.ThreadExternalID = "post:"+link, "Shared a post", text, link
		ev.Metadata["url"], ev.Metadata["visibility"] = link, "MEMBER_NETWORK"
		if i%3 == 0 {
			s := sites[g.rng.Intn(len(sites))]
			ev.Metadata["shared_url"] = s.URL
			ev.Targets = []domain.Target{{Relation: domain.RelationShared, TargetURL: s.URL}}
		}
		g.add(ev)
	}
	for i := 0; i < 8; i++ { // a second helping of posts so the type is not a one-off
		ev := base("linkedin.post", domain.CategoryContent, "Shares.csv", g.since(2018))
		link := fmt.Sprintf("https://www.linkedin.com/feed/update/urn%%3Ali%%3Ashare%%3A70000000000%08d", 20000000+i)
		ev.ExternalID, ev.Title, ev.Body, ev.ThreadExternalID = "post:"+link, "Shared a post", g.pick(tweets), link
		ev.Metadata["url"], ev.Metadata["visibility"] = link, "MEMBER_NETWORK"
		g.add(ev)
	}
	for i := 0; i < 30; i++ {
		urn, link := activity(fmt.Sprint("c", i))
		ev := base("linkedin.comment", domain.CategoryContent, "Comments.csv", g.since(2016))
		ev.ExternalID, ev.Title, ev.Body = fmt.Sprintf("comment:%d:%s", ev.OccurredAt.Unix(), short(link)[:12]), "Commented on a post", g.pick(linkedinComments)
		ev.Targets = []domain.Target{{Relation: domain.RelationRepliedTo, TargetSource: src, TargetExternalID: urn, TargetURL: link}}
		ev.Metadata["url"] = link
		g.add(ev)
	}
	kinds := []string{"LIKE", "LIKE", "LIKE", "PRAISE", "EMPATHY", "INTEREST", "APPRECIATION"}
	for i := 0; i < 60; i++ {
		urn, link := activity(fmt.Sprint("r", i))
		if i%6 == 0 && len(shares) > 0 { // a reaction on one of the persona's own posts
			link = shares[g.rng.Intn(len(shares))]
			urn = strings.TrimPrefix(link, "https://www.linkedin.com/feed/update/")
		}
		kind := g.pick(kinds)
		ev := base("linkedin.reaction", domain.CategoryReaction, "Reactions.csv", g.since(2016))
		ev.ExternalID, ev.Title = fmt.Sprintf("reaction:%d:%s", ev.OccurredAt.Unix(), short(kind + link)[:12]), "Reacted with "+kind
		ev.Targets = []domain.Target{{Relation: domain.RelationLiked, TargetSource: src, TargetExternalID: urn, TargetURL: link}}
		ev.Metadata["reaction"] = kind
		g.add(ev)
	}

	// network
	for i := 0; i < 80; i++ {
		var p person
		if i < len(cast) {
			p = cast[i]
		} else {
			p = person{Name: g.pick([]string{"Sam Sample", "Pat Placeholder", "Chris Doe", "Kim Example", "Ola Nordmann", "Joe Bloggs", "Juan Pérez", "Rita Recruiter", "Ivan Ivanov", "Pierre Dupont"}), Handle: fmt.Sprintf("member-%d", i)}
		}
		ev := base("linkedin.connection", domain.CategoryConnection, "Connections.csv", g.since(2015))
		ev.ExternalID, ev.Title = "connection:"+profileURL(p), "Connected with "+p.Name
		ev.Targets = []domain.Target{{Relation: domain.RelationFollowed, TargetSource: src, TargetExternalID: profileURL(p), TargetURL: profileURL(p)}}
		ev.Metadata["name"], ev.Metadata["company"], ev.Metadata["position"] = p.Name, g.pick(linkedinCompanies), g.pick(linkedinTitles)
		if i%4 == 0 {
			ev.Metadata["email"] = strings.ToLower(strings.ReplaceAll(p.Name, " ", ".")) + "@example.com"
		}
		g.add(ev)
	}
	for i := 0; i < 20; i++ {
		p := cast[(i*5)%len(cast)]
		ev := base("linkedin.invitation", domain.CategoryConnection, "Invitations.csv", g.since(2015))
		dir := "OUTGOING"
		if i%3 == 2 {
			dir = "INCOMING"
		}
		ev.ExternalID = fmt.Sprintf("invitation:%d:%s", ev.OccurredAt.Unix(), short(dir + p.Name)[:12])
		if dir == "OUTGOING" {
			ev.Title, ev.Body = "Invited "+p.Name+" to connect", g.pick([]string{"Great talk yesterday! Would love to stay in touch.", "Hi " + strings.Fields(p.Name)[0] + ", we met at GoDays — let's connect.", "Enjoyed our chat about data portability."})
			ev.Metadata["from"], ev.Metadata["to"] = self.Name, p.Name
		} else {
			ev.Title, ev.Body, ev.Actor = "Invitation to connect from "+p.Name, g.pick([]string{"I'd like to add you to my professional network.", "Saw your post on hexagonal architecture — great stuff.", "We have a role that might interest you."}), p.Name
			ev.Metadata["from"], ev.Metadata["to"] = p.Name, self.Name
		}
		ev.Metadata["direction"] = dir
		ev.Targets = []domain.Target{{Relation: domain.RelationFollowed, TargetSource: src, TargetExternalID: profileURL(p), TargetURL: profileURL(p)}}
		g.add(ev)
	}
	for i, name := range append(linkedinCompanies, "#golang", "#postgresql", "#selfhosted", "Max Mustermann", "Alice Example") {
		kind, link := "company", "https://www.linkedin.com/company/"+strings.ToLower(strings.ReplaceAll(name, " ", "-"))
		switch {
		case strings.HasPrefix(name, "#"):
			kind, link = "hashtag", "https://www.linkedin.com/feed/hashtag/"+name[1:]
		case i >= len(linkedinCompanies)+3:
			kind, link = "member", profileURL(person{Handle: strings.ToLower(strings.ReplaceAll(name, " ", "-"))})
		}
		ev := base("linkedin.follow", domain.CategoryConnection, "Company Follows.csv", g.since(2016))
		ev.ExternalID, ev.Title = fmt.Sprintf("follow:%s:%s", kind, short(name)[:12]), "Followed "+name
		ev.Targets = []domain.Target{{Relation: domain.RelationFollowed, TargetSource: src, TargetExternalID: name, TargetURL: link}}
		ev.Metadata["kind"] = kind
		if kind != "company" {
			ev.Metadata["member"] = map[string]string{"hashtag": "Hashtag_Follows.csv", "member": "Member_Follows.csv"}[kind]
			ev.Metadata["row"] = map[string]string{"date": ev.OccurredAt.Format("Mon Jan 02 15:04:05 UTC 2006"), "name": name, "link": link}
		}
		g.add(ev)
	}
	for i := 0; i < 15; i++ {
		urn, link := activity(fmt.Sprint("s", i))
		ev := base("linkedin.save", domain.CategorySave, "Saved_Items.csv", g.since(2019))
		ev.ExternalID, ev.Title = "save:"+link, "Saved "+urn
		ev.Targets = []domain.Target{{Relation: domain.RelationSaved, TargetSource: src, TargetExternalID: urn, TargetURL: link}}
		ev.Metadata["row"] = map[string]string{"savedItem": link, "savedItemDate": ev.OccurredAt.Format("2006-01-02")}
		g.add(ev)
	}
	for i := 0; i < 30; i++ {
		q := g.pick(linkedinSearches)
		ev := base("linkedin.search", domain.CategoryConsumption, "SearchQueries.csv", g.since(2017))
		ev.ExternalID, ev.Title, ev.Body = fmt.Sprintf("search:%d:%s", ev.OccurredAt.Unix(), short(q)[:12]), "Searched LinkedIn", q
		g.add(ev)
	}

	// profile
	type pos struct{ title, company, loc, start, end, desc string }
	for _, p := range []pos{
		{"Working Student", "Musterfirma AG", "Karlsruhe, Germany", "Oct 2014", "Sep 2016", "Test automation and the occasional bug fix in a Java monolith."},
		{"Software Engineer", "Example Corp", "Berlin, Germany", "Oct 2016", "Mar 2021", "Backend services in Go. Built the export pipeline and the job queue."},
		{"Senior Backend Engineer", "Example Data GmbH", "Berlin, Germany", "Apr 2021", "Jun 2025", "Owned the ingestion platform: Postgres, S3, one binary. Mentored two juniors."},
		{"Staff Engineer", "Example Data GmbH", "Berlin, Germany (Hybrid)", "Jul 2025", "", "Architecture across teams; data portability and self-hosting."},
	} {
		start, _ := time.Parse("Jan 2006", p.start)
		ev := base("linkedin.position", domain.CategoryProfile, "Positions.csv", start)
		ev.ExternalID, ev.Title, ev.Body = "position:"+short(p.company + p.title + p.start)[:12], p.title+" at "+p.company, p.desc
		ev.Metadata["company"], ev.Metadata["location"], ev.Metadata["started_on"] = p.company, p.loc, p.start
		if p.end != "" {
			ev.Metadata["finished_on"] = p.end
		}
		g.add(ev)
	}
	for _, e := range []struct{ school, degree, start, end, notes, act string }{
		{"Example University", "B.Sc. Computer Science", "2011", "2014", "Thesis on incremental parsing.", "Chess club, student radio"},
		{"Example University", "M.Sc. Computer Science", "2014", "2016", "Distributed systems track; thesis on idempotent ingestion.", "Teaching assistant, Databases"},
	} {
		start, _ := time.Parse("2006", e.start)
		ev := base("linkedin.education", domain.CategoryProfile, "Education.csv", start)
		ev.ExternalID, ev.Title, ev.Body = "education:"+short(e.school + e.degree + e.start)[:12], e.degree+" at "+e.school, e.notes
		ev.Metadata["school"], ev.Metadata["degree"], ev.Metadata["activities"], ev.Metadata["start_date"], ev.Metadata["end_date"] = e.school, e.degree, e.act, e.start, e.end
		g.add(ev)
	}

	// messages: recruiters and former colleagues
	convs := []struct {
		with  person
		title string
	}{
		{person{Name: "Rita Recruiter", Handle: "rita-recruiter"}, "An exciting opportunity"},
		{cast[3], "GoDays follow-up"},
		{cast[8], "Re: Postgres consulting"},
		{person{Name: "Sam Sample", Handle: "sam-sample"}, "Hiring"},
	}
	for c, cv := range convs {
		convID := fmt.Sprintf("2-%s==", short(cv.title)[:12])
		for i, l := range g.conversation(g.since(2019), []person{cv.with}, g.between(6, 18)) {
			ev := base("linkedin.message", domain.CategoryMessage, "messages.csv", l.At)
			ev.ExternalID = fmt.Sprintf("message:%s:%d:%s", convID, l.At.Unix(), short(l.Body + fmt.Sprint(i))[:12])
			ev.Title, ev.Body, ev.ThreadExternalID = cv.title, l.Body, convID
			if c == 0 && i == 0 {
				ev.Body = "Hi Jane, I came across your profile and think you'd be a great fit for a Staff Engineer role at Acme Analytics. Would you be open to a quick chat this week?"
			}
			ev.Metadata["conversation_title"], ev.Metadata["folder"] = cv.title, "INBOX"
			ev.Metadata["to"], ev.Metadata["sender_url"], ev.Metadata["recipient_urls"] = cv.with.Name, "https://www.linkedin.com/in/jane-doe-example", profileURL(cv.with)
			if !l.Self {
				ev.Actor, ev.Metadata["to"] = cv.with.Name, self.Name
				ev.Metadata["sender_url"], ev.Metadata["recipient_urls"] = profileURL(cv.with), "https://www.linkedin.com/in/jane-doe-example"
			}
			g.add(ev)
		}
	}
	draft := base("linkedin.message", domain.CategoryMessage, "messages.csv", g.since(2025))
	draft.ExternalID, draft.Title, draft.Body, draft.ThreadExternalID = "message:2-draft==:"+fmt.Sprint(draft.OccurredAt.Unix()), "Draft: Speaking at GoDays 2027?", "Hi Alice — the CfP closes next week; want to co-present the portability talk?", "2-draft=="
	draft.Metadata["to"], draft.Metadata["draft"], draft.Metadata["folder"] = cast[3].Name, true, "INBOX"
	g.add(draft)

	g.add(g.unknown(src, "Skills.csv", "Name\nGo\nPostgreSQL\nSvelte\nDistributed Systems\nSourdough\n", 74))
	g.add(g.unknown(src, "Endorsement_Received_Info.csv", "Endorsement Date,Skill Name,Endorser First Name,Endorser Last Name,Endorsement Status\n2024-03-02,Go,Max,Mustermann,ACCEPTED\n", 1932))
	g.add(g.unknown(src, "Ad_Targeting.csv", "Company Names,Company Industries,Job Titles,Member Age,Member Skills,Member Interests\nExample Data GmbH,Software,Staff Engineer,30-34,\"Go, PostgreSQL\",\"Data portability\"\n", 402))
}
