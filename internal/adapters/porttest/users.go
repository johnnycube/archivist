package porttest

import (
	"errors"
	"sync"
	"testing"

	"github.com/johnnycube/archivist/internal/app"
	"github.com/johnnycube/archivist/internal/domain"
)

// RunUsers checks an app.Users implementation. factory returns a fresh, empty
// store.
func RunUsers(t *testing.T, factory func(t *testing.T) app.Users) {
	t.Run("create and look up", func(t *testing.T) {
		users := factory(t)
		u, err := users.Create(ctx, "me@example.com", "hash-1")
		if err != nil {
			t.Fatal(err)
		}
		if u.ID == "" || u.Email != "me@example.com" || u.PasswordHash != "hash-1" {
			t.Errorf("created user = %+v", u)
		}
		if got, ok, err := users.ByEmail(ctx, "me@example.com"); err != nil || !ok || got != u {
			t.Errorf("ByEmail = %+v,%v,%v; want %+v", got, ok, err, u)
		}
		if got, ok, err := users.ByID(ctx, u.ID); err != nil || !ok || got != u {
			t.Errorf("ByID = %+v,%v,%v; want %+v", got, ok, err, u)
		}
	})

	t.Run("unknown users are not found, not errors", func(t *testing.T) {
		users := factory(t)
		if _, ok, err := users.ByEmail(ctx, "nobody@example.com"); ok || err != nil {
			t.Errorf("ByEmail(unknown) = %v,%v; want false,nil", ok, err)
		}
		for _, id := range []string{unknownID, malformedID, ""} {
			if _, ok, err := users.ByID(ctx, id); ok || err != nil {
				t.Errorf("ByID(%q) = %v,%v; want false,nil", id, ok, err)
			}
			if err := users.UpdatePassword(ctx, id, "x"); err != nil {
				t.Errorf("UpdatePassword(%q) = %v; want a no-op", id, err)
			}
			if err := users.Delete(ctx, id); err != nil {
				t.Errorf("Delete(%q) = %v; want a no-op", id, err)
			}
		}
	})

	t.Run("identities link, look up, and belong to one user", func(t *testing.T) {
		users := factory(t)
		a, err := users.Create(ctx, "a@example.com", "hash")
		if err != nil {
			t.Fatal(err)
		}
		b, err := users.Create(ctx, "b@example.com", "")
		if err != nil {
			t.Fatal(err)
		}
		if _, ok, err := users.ByIdentity(ctx, "idp", "sub-1"); ok || err != nil {
			t.Errorf("ByIdentity(unlinked) = %v,%v; want false,nil", ok, err)
		}
		if err := users.LinkIdentity(ctx, a.ID, "idp", "sub-1", "a@example.com"); err != nil {
			t.Fatal(err)
		}
		if err := users.LinkIdentity(ctx, a.ID, "idp", "sub-1", ""); err != nil {
			t.Errorf("relinking the same identity to the same user = %v; want a no-op", err)
		}
		if got, ok, err := users.ByIdentity(ctx, "idp", "sub-1"); err != nil || !ok || got != a {
			t.Errorf("ByIdentity = %+v,%v,%v; want %+v", got, ok, err, a)
		}
		if _, ok, _ := users.ByIdentity(ctx, "other-idp", "sub-1"); ok {
			t.Error("the same subject at another provider is a different identity")
		}
		if err := users.LinkIdentity(ctx, b.ID, "idp", "sub-1", ""); !errors.Is(err, app.ErrIdentityTaken) {
			t.Errorf("linking a's identity to b = %v; want ErrIdentityTaken", err)
		}
		if err := users.LinkIdentity(ctx, b.ID, "idp", "sub-2", ""); err != nil {
			t.Errorf("a second identity for another user = %v", err)
		}
		for _, id := range []string{unknownID, malformedID, ""} {
			if err := users.LinkIdentity(ctx, id, "idp", "sub-3", ""); !errors.Is(err, app.ErrNotFound) {
				t.Errorf("LinkIdentity(user %q) = %v; want ErrNotFound", id, err)
			}
		}
	})

	t.Run("List returns every user by email", func(t *testing.T) {
		users := factory(t)
		if got, err := users.List(ctx); err != nil || got == nil || len(got) != 0 {
			t.Errorf("List of no users = %#v, %v; want empty, non-nil", got, err)
		}
		b, _ := users.Create(ctx, "b@example.com", "hash")
		a, _ := users.Create(ctx, "a@example.com", "")
		got, err := users.List(ctx)
		if err != nil || len(got) != 2 || got[0] != a || got[1] != b {
			t.Errorf("List = %+v, %v; want a then b", got, err)
		}
	})

	t.Run("LinkedProviders lists each provider once, sorted", func(t *testing.T) {
		users := factory(t)
		a, err := users.Create(ctx, "a@example.com", "hash")
		if err != nil {
			t.Fatal(err)
		}
		b, err := users.Create(ctx, "b@example.com", "hash")
		if err != nil {
			t.Fatal(err)
		}
		if got, err := users.LinkedProviders(ctx, a.ID); err != nil || got == nil || len(got) != 0 {
			t.Errorf("LinkedProviders(no links) = %#v,%v; want an empty, non-nil list", got, err)
		}
		for _, l := range []struct{ user, provider, subject string }{
			{a.ID, "zitadel", "1"}, {a.ID, "gitea", "1"}, {a.ID, "gitea", "2"}, {b.ID, "google", "1"},
		} {
			if err := users.LinkIdentity(ctx, l.user, l.provider, l.subject, ""); err != nil {
				t.Fatal(err)
			}
		}
		if got, err := users.LinkedProviders(ctx, a.ID); err != nil || len(got) != 2 || got[0] != "gitea" || got[1] != "zitadel" {
			t.Errorf("LinkedProviders(a) = %v,%v; want [gitea zitadel]", got, err)
		}
		for _, id := range []string{unknownID, malformedID, ""} {
			if got, err := users.LinkedProviders(ctx, id); err != nil || got == nil || len(got) != 0 {
				t.Errorf("LinkedProviders(%q) = %#v,%v; want an empty, non-nil list", id, got, err)
			}
		}
	})

	t.Run("email starts unverified; MarkEmailVerified sets it for that user only", func(t *testing.T) {
		users := factory(t)
		a, err := users.Create(ctx, "a@example.com", "hash")
		if err != nil {
			t.Fatal(err)
		}
		b, err := users.Create(ctx, "b@example.com", "")
		if err != nil {
			t.Fatal(err)
		}
		if a.EmailVerified || b.EmailVerified {
			t.Fatalf("new users verified: %+v %+v", a, b)
		}
		if err := users.MarkEmailVerified(ctx, a.ID); err != nil {
			t.Fatal(err)
		}
		if err := users.LinkIdentity(ctx, a.ID, "idp", "sub-a", ""); err != nil {
			t.Fatal(err)
		}
		if got, _, _ := users.ByID(ctx, a.ID); !got.EmailVerified {
			t.Error("ByID: email not verified after MarkEmailVerified")
		}
		if got, _, _ := users.ByEmail(ctx, "A@example.com"); !got.EmailVerified {
			t.Error("ByEmail: email not verified after MarkEmailVerified")
		}
		if got, _, _ := users.ByIdentity(ctx, "idp", "sub-a"); !got.EmailVerified {
			t.Error("ByIdentity: email not verified after MarkEmailVerified")
		}
		if got, _, _ := users.ByID(ctx, b.ID); got.EmailVerified {
			t.Error("MarkEmailVerified verified another user")
		}
		for _, id := range []string{unknownID, malformedID, ""} {
			if err := users.MarkEmailVerified(ctx, id); err != nil {
				t.Errorf("MarkEmailVerified(%q) = %v; want a no-op", id, err)
			}
		}
	})

	// The rule is domain.NormalizeEmail: case and surrounding space do not
	// distinguish accounts.
	t.Run("emails are case-insensitive and unique", func(t *testing.T) {
		users := factory(t)
		u, err := users.Create(ctx, "  Me@Example.COM ", "hash")
		if err != nil {
			t.Fatal(err)
		}
		if u.Email != "me@example.com" {
			t.Errorf("stored email = %q, want the normalized me@example.com", u.Email)
		}
		for _, dup := range []string{"me@example.com", "ME@EXAMPLE.COM", " me@Example.com"} {
			if _, err := users.Create(ctx, dup, "other"); !errors.Is(err, app.ErrEmailTaken) {
				t.Errorf("Create(%q) err = %v, want ErrEmailTaken", dup, err)
			}
		}
		for _, q := range []string{"me@example.com", "ME@example.com", "\tme@EXAMPLE.com "} {
			if got, ok, err := users.ByEmail(ctx, q); err != nil || !ok || got.ID != u.ID {
				t.Errorf("ByEmail(%q) = %+v,%v,%v; want user %s", q, got, ok, err, u.ID)
			}
		}
		if got, _, _ := users.ByID(ctx, u.ID); got.PasswordHash != "hash" {
			t.Errorf("a refused duplicate changed the password hash to %q", got.PasswordHash)
		}
	})

	t.Run("UpdatePassword changes only that user", func(t *testing.T) {
		users := factory(t)
		a, _ := users.Create(ctx, "a@example.com", "hash-a")
		b, _ := users.Create(ctx, "b@example.com", "hash-b")
		if err := users.UpdatePassword(ctx, a.ID, "hash-a2"); err != nil {
			t.Fatal(err)
		}
		if got, _, _ := users.ByID(ctx, a.ID); got.PasswordHash != "hash-a2" {
			t.Errorf("ByID hash = %q, want hash-a2", got.PasswordHash)
		}
		if got, _, _ := users.ByEmail(ctx, "a@example.com"); got.PasswordHash != "hash-a2" {
			t.Errorf("ByEmail hash = %q, want hash-a2", got.PasswordHash)
		}
		if got, _, _ := users.ByID(ctx, b.ID); got.PasswordHash != "hash-b" {
			t.Errorf("other user's hash = %q, want hash-b untouched", got.PasswordHash)
		}
	})

	t.Run("Delete removes only that user and frees the email", func(t *testing.T) {
		users := factory(t)
		a, _ := users.Create(ctx, "a@example.com", "hash-a")
		b, _ := users.Create(ctx, "b@example.com", "hash-b")
		if err := users.Delete(ctx, a.ID); err != nil {
			t.Fatal(err)
		}
		if _, ok, _ := users.ByID(ctx, a.ID); ok {
			t.Error("deleted user still found by id")
		}
		if _, ok, _ := users.ByEmail(ctx, "a@example.com"); ok {
			t.Error("deleted user still found by email")
		}
		if _, ok, _ := users.ByID(ctx, b.ID); !ok {
			t.Error("deleting one user removed another")
		}
		again, err := users.Create(ctx, "A@example.com", "hash-a3")
		if err != nil {
			t.Fatalf("re-register a deleted email: %v", err)
		}
		if again.ID == a.ID {
			t.Error("a re-registered email got the deleted user's id back")
		}
	})
}

// RunAccounts checks an app.Accounts implementation.
func RunAccounts(t *testing.T, factory func(t *testing.T) (app.Accounts, Env)) {
	t.Run("Ensure is idempotent per (user, source, label)", func(t *testing.T) {
		accounts, env := factory(t)
		alice, bob := env.NewUser(t), env.NewUser(t)
		id, err := accounts.Ensure(ctx, alice, "reddit", "main")
		if err != nil || id == "" {
			t.Fatalf("Ensure = %q,%v", id, err)
		}
		if again, err := accounts.Ensure(ctx, alice, "reddit", "main"); err != nil || again != id {
			t.Errorf("second Ensure = %q,%v; want the same id %q", again, err, id)
		}
		distinct := map[string]string{"first": id}
		for name, args := range map[string][3]string{
			"other label":  {alice, "reddit", "work"},
			"other source": {alice, "twitter", "main"},
			"other user":   {bob, "reddit", "main"},
		} {
			got, err := accounts.Ensure(ctx, args[0], args[1], args[2])
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			for prev, prevID := range distinct {
				if got == prevID {
					t.Errorf("%s shares account %q with %s", name, got, prev)
				}
			}
			distinct[name] = got
		}
	})

	t.Run("an empty label means default", func(t *testing.T) {
		accounts, env := factory(t)
		u := env.NewUser(t)
		a, err := accounts.Ensure(ctx, u, "reddit", "")
		if err != nil {
			t.Fatal(err)
		}
		if b, _ := accounts.Ensure(ctx, u, "reddit", "default"); a != b {
			t.Errorf(`Ensure(label "") = %q, Ensure(label "default") = %q; want the same account`, a, b)
		}
	})

	// Select-then-insert made two accounts when two imports of one connection
	// started together. Run with -race.
	t.Run("concurrent Ensure yields one account", func(t *testing.T) {
		accounts, env := factory(t)
		u := env.NewUser(t)
		const n = 24
		ids := make([]string, n)
		var wg sync.WaitGroup
		start := make(chan struct{})
		for i := range ids {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				id, err := accounts.Ensure(ctx, u, "reddit", "main")
				if err != nil {
					t.Errorf("Ensure: %v", err)
				}
				ids[i] = id
			}()
		}
		close(start)
		wg.Wait()
		for i, id := range ids {
			if id == "" || id != ids[0] {
				t.Fatalf("caller %d got account %q, caller 0 got %q; want one account for all", i, id, ids[0])
			}
		}
		if again, _ := accounts.Ensure(ctx, u, "reddit", "main"); again != ids[0] {
			t.Errorf("Ensure afterwards = %q, want %q", again, ids[0])
		}
	})
}

// Store is every port of one store, for the contract that spans them.
type Store struct {
	Users    app.Users
	Accounts app.Accounts
	Events   app.EventRepository
	Jobs     app.Jobs
	Sessions app.Sessions
}

// RunUserDeletion checks that Users.Delete takes everything the user owns with
// it, in every port of the same store, and nothing of anybody else. factory
// returns a fresh, empty store (sessions: any TTL of an hour or more).
func RunUserDeletion(t *testing.T, factory func(t *testing.T) Store) {
	s := factory(t)
	env := EnvFrom(s.Users, s.Accounts, s.Jobs)
	gone, stays := env.NewUser(t), env.NewUser(t)
	jobs := map[string]string{}
	for _, u := range []string{gone, stays} {
		acct := env.NewAccount(t, u, "reddit")
		jobs[u] = env.NewJob(t, u)
		// A queued job as well: the queue must not hand out a deleted user's job.
		if _, err := s.Jobs.Create(ctx, domain.ImportJob{
			UserID: u, Source: "reddit", Label: "porttest",
			ZipPaths: []string{"ref-" + u}, ArchiveBytes: 10, CreatedAt: t0, UpdatedAt: t0,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Events.SaveBatch(ctx, u, acct, app.ImportRun{JobID: jobs[u]}, []domain.Event{{
			SourceID: "reddit", ExternalID: "post:1", Type: "reddit.post", Category: domain.CategoryContent,
			Title: "hello", OccurredAt: t0,
			Targets: []domain.Target{{Relation: domain.RelationSaved, TargetURL: "http://x"}},
			Media:   []domain.Media{{BlobKey: "m.jpg"}},
		}}); err != nil {
			t.Fatal(err)
		}
		if err := s.Sessions.Create(ctx, "hash-"+u, u, t0); err != nil {
			t.Fatal(err)
		}
		if err := s.Users.LinkIdentity(ctx, u, "idp", "sub-"+u, ""); err != nil {
			t.Fatal(err)
		}
	}

	if err := s.Users.Delete(ctx, gone); err != nil {
		t.Fatal(err)
	}
	for u, want := range map[string]bool{gone: false, stays: true} {
		if _, ok, err := s.Users.ByIdentity(ctx, "idp", "sub-"+u); err != nil || ok != want {
			t.Errorf("Users.ByIdentity after delete = %v,%v; want found = %v", ok, err, want)
		}
	}

	for u, want := range map[string]int{gone: 0, stays: 1} {
		page, err := s.Events.Timeline(ctx, app.Query{UserID: u, Limit: 10})
		if err != nil || page.Total != want || len(page.Events) != want {
			t.Errorf("user with %d expected events: Timeline = %d events (total %d), %v", want, len(page.Events), page.Total, err)
		}
		if hist, err := s.Events.Histogram(ctx, app.Query{UserID: u}); err != nil || len(hist) != want {
			t.Errorf("user with %d expected events: Histogram = %v, %v", want, hist, err)
		}
		if list, err := s.Jobs.List(ctx, u, 10); err != nil || len(list) != 2*want {
			t.Errorf("user with %d expected jobs: List = %d jobs, %v", 2*want, len(list), err)
		}
		if _, ok, err := s.Jobs.Get(ctx, u, jobs[u]); err != nil || ok != (want == 1) {
			t.Errorf("Jobs.Get = %v,%v; want found = %v", ok, err, want == 1)
		}
		if use, err := s.Jobs.Usage(ctx, u); err != nil || use.Active != want || use.ArchiveBytes != int64(10*want) {
			t.Errorf("Usage = %+v,%v; want %d active", use, err, want)
		}
		if _, ok, err := s.Sessions.Lookup(ctx, "hash-"+u, t0); err != nil || ok != (want == 1) {
			t.Errorf("Sessions.Lookup = %v,%v; want found = %v", ok, err, want == 1)
		}
	}
	if refs, err := s.Jobs.StagedRefs(ctx); err != nil || len(refs) != 1 || refs[0] != "ref-"+stays {
		t.Errorf("StagedRefs = %v,%v; want only the remaining user's", refs, err)
	}
	// The queue holds exactly the remaining user's job.
	if j, ok, err := s.Jobs.Claim(ctx); err != nil || !ok || j.UserID != stays {
		t.Errorf("Claim = job of %q, %v, %v; want the remaining user's", j.UserID, ok, err)
	}
	if j, ok, _ := s.Jobs.Claim(ctx); ok {
		t.Errorf("Claim handed out a job of deleted user %q", j.UserID)
	}
	// The remaining user's account is still the one their events hang off.
	page, _ := s.Events.Timeline(ctx, app.Query{UserID: stays, Limit: 10})
	if acct := env.NewAccount(t, stays, "reddit"); len(page.Events) == 1 && page.Events[0].AccountID != acct {
		t.Errorf("remaining user's account changed: event on %q, Ensure = %q", page.Events[0].AccountID, acct)
	}
}
