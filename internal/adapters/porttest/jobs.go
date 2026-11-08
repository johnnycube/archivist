package porttest

import (
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/johnnycube/archivist/internal/app"
	"github.com/johnnycube/archivist/internal/domain"
)

// RunJobs checks an app.Jobs implementation. factory returns a fresh store
// with an EMPTY queue (Claim is global, so leftovers would be claimed).
func RunJobs(t *testing.T, factory func(t *testing.T) (app.Jobs, Env)) {
	// newJob enqueues a job created `age` after t0.
	newJob := func(t *testing.T, jobs app.Jobs, userID string, age time.Duration) domain.ImportJob {
		t.Helper()
		at := t0.Add(age)
		j, err := jobs.Create(ctx, domain.ImportJob{
			UserID: userID, Source: "reddit", Label: "main", ZipPaths: []string{"ref-1"},
			ArchiveBytes: 100, Status: domain.JobQueued, CreatedAt: at, UpdatedAt: at,
		})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		return j
	}
	get := func(t *testing.T, jobs app.Jobs, userID, id string) domain.ImportJob {
		t.Helper()
		j, ok, err := jobs.Get(ctx, userID, id)
		if err != nil || !ok {
			t.Fatalf("Get(%s) = ok %v, err %v", id, ok, err)
		}
		return j
	}
	claim := func(t *testing.T, jobs app.Jobs) domain.ImportJob {
		t.Helper()
		j, ok, err := jobs.Claim(ctx)
		if err != nil || !ok {
			t.Fatalf("Claim = ok %v, err %v; want a job", ok, err)
		}
		return j
	}

	t.Run("Create stores the job as given and assigns the id", func(t *testing.T) {
		jobs, env := factory(t)
		u := env.NewUser(t)
		at := t0.Add(-48 * time.Hour) // not "now": the supplied clock must win
		in := domain.ImportJob{
			UserID: u, Source: "reddit", Label: "work", ZipPaths: []string{"ref-a", "ref-b"},
			PartNames:    []string{"WhatsApp Chat - Ünï \"quoted\".zip", ""},
			ArchiveBytes: 1234, Status: domain.JobQueued, CreatedAt: at, UpdatedAt: at,
		}
		made, err := jobs.Create(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		if made.ID == "" {
			t.Fatal("Create assigned no id")
		}
		stored := get(t, jobs, u, made.ID)
		for name, j := range map[string]domain.ImportJob{"returned": made, "stored": stored} {
			if j.ID != made.ID || j.UserID != u || j.Source != "reddit" || j.Label != "work" ||
				j.ArchiveBytes != 1234 || j.Status != domain.JobQueued || j.Attempts != 0 {
				t.Errorf("%s job = %+v", name, j)
			}
			if len(j.ZipPaths) != 2 || j.ZipPaths[0] != "ref-a" || j.ZipPaths[1] != "ref-b" || j.Parts != 2 {
				t.Errorf("%s job parts = %v (Parts %d), want both refs in order and Parts 2", name, j.ZipPaths, j.Parts)
			}
			if !slices.Equal(j.PartNames, in.PartNames) || j.PartName(0) != in.PartNames[0] || j.PartName(1) != "" || j.PartName(2) != "" {
				t.Errorf("%s job part names = %q, want %q", name, j.PartNames, in.PartNames)
			}
			if !j.CreatedAt.Equal(at) || !j.UpdatedAt.Equal(at) {
				t.Errorf("%s job timestamps = %v / %v, want the supplied %v", name, j.CreatedAt, j.UpdatedAt, at)
			}
		}
		other, err := jobs.Create(ctx, in)
		if err != nil || other.ID == made.ID {
			t.Errorf("second Create = id %q, err %v; want a new id", other.ID, err)
		}
	})

	t.Run("Create fills in what the caller left out", func(t *testing.T) {
		jobs, env := factory(t)
		u := env.NewUser(t)
		made, err := jobs.Create(ctx, domain.ImportJob{UserID: u, Source: "reddit", Label: "main"})
		if err != nil {
			t.Fatal(err)
		}
		j := get(t, jobs, u, made.ID)
		if j.Status != domain.JobQueued {
			t.Errorf("default status = %q, want queued", j.Status)
		}
		if j.CreatedAt.IsZero() || j.UpdatedAt.IsZero() {
			t.Errorf("timestamps = %v / %v, want the store's clock", j.CreatedAt, j.UpdatedAt)
		}
		if j.Parts != 0 || len(j.ZipPaths) != 0 {
			t.Errorf("parts = %v (%d), want none", j.ZipPaths, j.Parts)
		}
		if len(j.PartNames) != 0 || j.PartName(0) != "" {
			t.Errorf("part names = %q, want none", j.PartNames)
		}
	})

	t.Run("CreateWithin enforces both quotas and creates like Create", func(t *testing.T) {
		jobs, env := factory(t)
		alice, bob := env.NewUser(t), env.NewUser(t)
		limits := app.Limits{MaxActiveImports: 2, MaxArchiveBytes: 250}
		job := func(u string, bytes int64) domain.ImportJob {
			return domain.ImportJob{
				UserID: u, Source: "reddit", Label: "main", ZipPaths: []string{"r"},
				PartNames: []string{"export.zip"}, ArchiveBytes: bytes, CreatedAt: t0, UpdatedAt: t0,
			}
		}
		first, err := jobs.CreateWithin(ctx, job(alice, 100), limits)
		if err != nil {
			t.Fatal(err)
		}
		if g := get(t, jobs, alice, first.ID); g.Status != domain.JobQueued || g.ArchiveBytes != 100 || g.Parts != 1 ||
			g.PartName(0) != "export.zip" || !g.CreatedAt.Equal(t0) || g.ID != first.ID {
			t.Errorf("job made by CreateWithin = %+v", g)
		}
		if _, err := jobs.CreateWithin(ctx, job(alice, 151), limits); !errors.Is(err, app.ErrStorageQuota) || !errors.Is(err, app.ErrQuotaExceeded) {
			t.Errorf("100 + 151 of 250 bytes: err = %v, want ErrStorageQuota", err)
		}
		if _, err := jobs.CreateWithin(ctx, job(alice, 150), limits); err != nil {
			t.Errorf("100 + 150 of 250 bytes (exactly the limit): %v", err)
		}
		if _, err := jobs.CreateWithin(ctx, job(alice, 0), limits); !errors.Is(err, app.ErrTooManyImports) || !errors.Is(err, app.ErrQuotaExceeded) {
			t.Errorf("third active import of 2: err = %v, want ErrTooManyImports", err)
		}
		if list, _ := jobs.List(ctx, alice, 10); len(list) != 2 {
			t.Errorf("alice has %d jobs, want 2: a refused admission must leave nothing behind", len(list))
		}
		// Quotas are per user.
		if _, err := jobs.CreateWithin(ctx, job(bob, 250), limits); err != nil {
			t.Errorf("bob's first import, with alice at her limits: %v", err)
		}
		// A finished job frees an active slot, but its archive still counts.
		claim(t, jobs)
		_ = jobs.Complete(ctx, first.ID, domain.JobDone, 0, nil, "")
		if _, err := jobs.CreateWithin(ctx, job(alice, 1), limits); !errors.Is(err, app.ErrStorageQuota) {
			t.Errorf("after one import finished: err = %v, want ErrStorageQuota (250 of 250 used)", err)
		}
		if _, err := jobs.CreateWithin(ctx, job(alice, 0), limits); err != nil {
			t.Errorf("after one import finished, a 0-byte import: %v", err)
		}
		// Zero limits are unlimited.
		for i := 0; i < 5; i++ {
			if _, err := jobs.CreateWithin(ctx, job(alice, 1000), app.Limits{}); err != nil {
				t.Fatalf("unlimited CreateWithin: %v", err)
			}
		}
	})

	// The reason CreateWithin exists: check-then-create let concurrent uploads
	// of one user all pass the check. Run with -race.
	t.Run("concurrent CreateWithin never overshoots a quota", func(t *testing.T) {
		for name, tc := range map[string]struct {
			limits app.Limits
			want   int
		}{
			"active imports": {app.Limits{MaxActiveImports: 3}, 3},
			"archive bytes":  {app.Limits{MaxArchiveBytes: 350}, 3}, // 100 bytes each
		} {
			t.Run(name, func(t *testing.T) {
				jobs, env := factory(t)
				u, other := env.NewUser(t), env.NewUser(t)
				const n = 16
				var (
					wg      sync.WaitGroup
					start   = make(chan struct{})
					mu      sync.Mutex
					created int
				)
				for i := 0; i < n; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						<-start
						_, err := jobs.CreateWithin(ctx, domain.ImportJob{
							UserID: u, Source: "reddit", Label: "main", ZipPaths: []string{"r"}, ArchiveBytes: 100,
						}, tc.limits)
						switch {
						case err == nil:
							mu.Lock()
							created++
							mu.Unlock()
						case !errors.Is(err, app.ErrQuotaExceeded):
							t.Errorf("CreateWithin: %v", err)
						}
					}()
				}
				// Another user's admission runs through the middle of it all.
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					if _, err := jobs.CreateWithin(ctx, domain.ImportJob{UserID: other, Source: "reddit", Label: "main", ArchiveBytes: 100}, tc.limits); err != nil {
						t.Errorf("other user's CreateWithin: %v", err)
					}
				}()
				close(start)
				wg.Wait()
				use, err := jobs.Usage(ctx, u)
				if err != nil {
					t.Fatal(err)
				}
				if created != tc.want || use.Active != tc.want || use.ArchiveBytes != int64(tc.want)*100 {
					t.Errorf("%d of %d concurrent admissions succeeded, usage %+v; want exactly %d", created, n, use, tc.want)
				}
			})
		}
	})

	t.Run("StagedRefs lists the refs of every job of every user", func(t *testing.T) {
		jobs, env := factory(t)
		if refs, err := jobs.StagedRefs(ctx); err != nil || len(refs) != 0 {
			t.Errorf("StagedRefs without jobs = %v,%v; want none", refs, err)
		}
		alice, bob := env.NewUser(t), env.NewUser(t)
		mk := func(u string, refs ...string) domain.ImportJob {
			j, err := jobs.Create(ctx, domain.ImportJob{UserID: u, Source: "reddit", Label: "main", ZipPaths: refs})
			if err != nil {
				t.Fatal(err)
			}
			return j
		}
		mk(alice, "a/1.zip", "a/2.zip")
		done := mk(bob, "b/1.zip")
		mk(bob, "b/2.zip") // stays queued
		mk(alice)          // no parts
		claim(t, jobs)     // one of them runs…
		claim(t, jobs)
		_ = jobs.Complete(ctx, done.ID, domain.JobDone, 0, nil, "") // …and bob's first finishes (whether or not it was claimed)
		refs, err := jobs.StagedRefs(ctx)
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(refs)
		if want := []string{"a/1.zip", "a/2.zip", "b/1.zip", "b/2.zip"}; !slices.Equal(refs, want) {
			t.Errorf("StagedRefs = %v, want %v whatever the jobs' status", refs, want)
		}
		_, _ = jobs.Delete(ctx, bob, done.ID)
		refs, _ = jobs.StagedRefs(ctx)
		if slices.Contains(refs, "b/1.zip") || len(refs) != 3 {
			t.Errorf("StagedRefs after deleting a job = %v, want its ref gone", refs)
		}
	})

	t.Run("unknown and malformed ids are not found, not errors", func(t *testing.T) {
		jobs, env := factory(t)
		u := env.NewUser(t)
		for _, id := range []string{unknownID, malformedID, ""} {
			if _, ok, err := jobs.Get(ctx, u, id); ok || err != nil {
				t.Errorf("Get(%q) = %v,%v; want false,nil", id, ok, err)
			}
			if _, ok, err := jobs.Requeue(ctx, u, id); ok || err != nil {
				t.Errorf("Requeue(%q) = %v,%v; want false,nil", id, ok, err)
			}
			if out, err := jobs.Delete(ctx, u, id); out != app.JobNotFound || err != nil {
				t.Errorf("Delete(%q) = %v,%v; want JobNotFound,nil", id, out, err)
			}
		}
	})

	t.Run("tenant isolation", func(t *testing.T) {
		jobs, env := factory(t)
		alice, bob := env.NewUser(t), env.NewUser(t)
		aj := newJob(t, jobs, alice, 0)
		claim(t, jobs)
		if err := jobs.Complete(ctx, aj.ID, domain.JobDone, 3, nil, ""); err != nil {
			t.Fatal(err)
		}
		before := get(t, jobs, alice, aj.ID)

		if _, ok, err := jobs.Get(ctx, bob, aj.ID); ok || err != nil {
			t.Errorf("bob Get(alice's job) = %v,%v; want false,nil", ok, err)
		}
		if _, ok, err := jobs.Requeue(ctx, bob, aj.ID); ok || err != nil {
			t.Errorf("bob Requeue(alice's job) = %v,%v; want false,nil", ok, err)
		}
		if out, err := jobs.Delete(ctx, bob, aj.ID); out != app.JobNotFound || err != nil {
			t.Errorf("bob Delete(alice's job) = %v,%v; want JobNotFound,nil", out, err)
		}
		if list, err := jobs.List(ctx, bob, 10); err != nil || len(list) != 0 {
			t.Errorf("bob List = %v,%v; want none", list, err)
		}
		if use, err := jobs.Usage(ctx, bob); err != nil || use != (app.JobUsage{}) {
			t.Errorf("bob Usage = %+v,%v; want zero", use, err)
		}
		after := get(t, jobs, alice, aj.ID)
		if after.Status != before.Status || after.Attempts != before.Attempts || after.Saved != before.Saved {
			t.Errorf("bob's attempts changed alice's job: %+v -> %+v", before, after)
		}
	})

	t.Run("List is newest first and limited", func(t *testing.T) {
		jobs, env := factory(t)
		u := env.NewUser(t)
		// Inserted out of order on purpose.
		mid := newJob(t, jobs, u, 2*time.Minute)
		old := newJob(t, jobs, u, 1*time.Minute)
		newest := newJob(t, jobs, u, 3*time.Minute)
		list, err := jobs.List(ctx, u, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 3 || list[0].ID != newest.ID || list[1].ID != mid.ID || list[2].ID != old.ID {
			t.Errorf("List order = %v, want newest, mid, old", jobIDs(list))
		}
		if list, _ := jobs.List(ctx, u, 2); len(list) != 2 || list[0].ID != newest.ID {
			t.Errorf("List(limit 2) = %v, want the 2 newest", jobIDs(list))
		}
		for _, limit := range []int{0, -1} {
			if list, err := jobs.List(ctx, u, limit); err != nil || len(list) != 0 {
				t.Errorf("List(limit %d) = %v,%v; want none, nil", limit, jobIDs(list), err)
			}
		}
	})

	t.Run("Claim hands out queued jobs oldest first, across users", func(t *testing.T) {
		jobs, env := factory(t)
		alice, bob := env.NewUser(t), env.NewUser(t)
		second := newJob(t, jobs, alice, 2*time.Minute)
		third := newJob(t, jobs, bob, 3*time.Minute)
		first := newJob(t, jobs, bob, 1*time.Minute)
		for i, want := range []domain.ImportJob{first, second, third} {
			got := claim(t, jobs)
			if got.ID != want.ID {
				t.Fatalf("claim %d = job created %v, want the one created %v", i+1, got.CreatedAt, want.CreatedAt)
			}
			if got.Status != domain.JobRunning || got.Attempts != 1 || got.UserID != want.UserID ||
				len(got.ZipPaths) != 1 || got.Parts != 1 || got.ArchiveBytes != 100 {
				t.Errorf("claimed job = %+v; want running, attempts 1, its archive refs", got)
			}
			if stored := get(t, jobs, want.UserID, want.ID); stored.Status != domain.JobRunning {
				t.Errorf("stored status after Claim = %q, want running", stored.Status)
			}
		}
		if _, ok, err := jobs.Claim(ctx); ok || err != nil {
			t.Errorf("Claim on a drained queue = %v,%v; want false,nil", ok, err)
		}
	})

	// Audit H3: one worker serves all tenants, so FIFO let one user's backlog
	// block everybody. Claim is round-robin by user.
	t.Run("Claim: A1 A2 A3 then B1 is served A1, B1, A2, A3", func(t *testing.T) {
		jobs, env := factory(t)
		alice, bob := env.NewUser(t), env.NewUser(t)
		a1 := newJob(t, jobs, alice, 1*time.Minute)
		a2 := newJob(t, jobs, alice, 2*time.Minute)
		a3 := newJob(t, jobs, alice, 3*time.Minute)
		b1 := newJob(t, jobs, bob, 4*time.Minute)
		for i, want := range []domain.ImportJob{a1, b1, a2, a3} {
			if got := claim(t, jobs); got.ID != want.ID {
				t.Fatalf("claim %d = the job created at +%v, want +%v", i+1, got.CreatedAt.Sub(t0), want.CreatedAt.Sub(t0))
			}
		}
	})

	t.Run("Claim is fair across users", func(t *testing.T) {
		jobs, env := factory(t)
		alice, bob, carol := env.NewUser(t), env.NewUser(t), env.NewUser(t)
		a1 := newJob(t, jobs, alice, 1*time.Minute)
		a2 := newJob(t, jobs, alice, 2*time.Minute)
		a3 := newJob(t, jobs, alice, 3*time.Minute)
		b1 := newJob(t, jobs, bob, 4*time.Minute)
		order := func(want ...domain.ImportJob) {
			t.Helper()
			for i, w := range want {
				if got := claim(t, jobs); got.ID != w.ID {
					t.Fatalf("claim %d = the job of %v, want the one of %v", i+1, got.CreatedAt.Sub(t0), w.CreatedAt.Sub(t0))
				}
			}
		}
		// Nobody was served yet: oldest first. Then bob — never served —
		// overtakes alice's backlog.
		order(a1, b1)
		// Both were served, alice longer ago. A newcomer still goes first, and a
		// requeued job is queued like any other.
		c1 := newJob(t, jobs, carol, 9*time.Minute)
		_ = jobs.Complete(ctx, b1.ID, domain.JobDone, 0, nil, "")
		if _, _, err := jobs.Requeue(ctx, bob, b1.ID); err != nil {
			t.Fatal(err)
		}
		order(c1, a2, b1, a3)
		if _, ok, err := jobs.Claim(ctx); ok || err != nil {
			t.Errorf("Claim on a drained queue = %v,%v; want false,nil", ok, err)
		}
	})

	t.Run("concurrent claimers never share a job", func(t *testing.T) {
		jobs, env := factory(t)
		u := env.NewUser(t)
		const nJobs, nWorkers = 40, 8
		want := map[string]bool{}
		for i := 0; i < nJobs; i++ {
			want[newJob(t, jobs, u, time.Duration(i)*time.Second).ID] = true
		}
		var (
			mu      sync.Mutex
			claimed = map[string]int{}
			wg      sync.WaitGroup
			start   = make(chan struct{})
		)
		for w := 0; w < nWorkers; w++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				for {
					j, ok, err := jobs.Claim(ctx)
					if err != nil {
						t.Errorf("Claim: %v", err)
						return
					}
					if !ok {
						return
					}
					mu.Lock()
					claimed[j.ID]++
					mu.Unlock()
				}
			}()
		}
		close(start)
		wg.Wait()
		if len(claimed) != nJobs {
			t.Errorf("%d distinct jobs claimed, want all %d", len(claimed), nJobs)
		}
		for id, n := range claimed {
			if n != 1 {
				t.Errorf("job %s was claimed %d times", id, n)
			}
			if !want[id] {
				t.Errorf("claimed a job nobody enqueued: %s", id)
			}
		}
	})

	t.Run("Progress updates a running job only", func(t *testing.T) {
		jobs, env := factory(t)
		u := env.NewUser(t)
		j := newJob(t, jobs, u, 0)
		if err := jobs.Progress(ctx, j.ID, 9, 9, 9); err != nil {
			t.Fatal(err)
		}
		if g := get(t, jobs, u, j.ID); g.Saved != 0 || g.BytesDone != 0 || g.BytesTotal != 0 {
			t.Errorf("Progress changed a queued job: %+v", g)
		}
		claim(t, jobs)
		if err := jobs.Progress(ctx, j.ID, 2, 100, 400); err != nil {
			t.Fatal(err)
		}
		if g := get(t, jobs, u, j.ID); g.Saved != 2 || g.BytesDone != 100 || g.BytesTotal != 400 {
			t.Errorf("progress = saved %d, %d/%d; want 2, 100/400", g.Saved, g.BytesDone, g.BytesTotal)
		}
		_ = jobs.Complete(ctx, j.ID, domain.JobDone, 5, nil, "")
		_ = jobs.Progress(ctx, j.ID, 7, 7, 7)
		if g := get(t, jobs, u, j.ID); g.Saved != 5 {
			t.Errorf("Progress changed a finished job: saved = %d, want 5", g.Saved)
		}
		if err := jobs.Progress(ctx, unknownID, 1, 1, 1); err != nil {
			t.Errorf("Progress(unknown) = %v, want a no-op", err)
		}
	})

	t.Run("Complete records the outcome and resets the crash counter", func(t *testing.T) {
		jobs, env := factory(t)
		u := env.NewUser(t)
		ok := newJob(t, jobs, u, 0)
		bad := newJob(t, jobs, u, time.Minute)
		claim(t, jobs)
		claim(t, jobs)
		cov := &domain.Coverage{Events: 5, New: 4, Reimported: 1, PerType: map[string]int{"reddit.post": 5}}
		if err := jobs.Complete(ctx, ok.ID, domain.JobDone, 5, cov, ""); err != nil {
			t.Fatal(err)
		}
		if err := jobs.Complete(ctx, bad.ID, domain.JobError, 1, nil, "boom"); err != nil {
			t.Fatal(err)
		}
		g := get(t, jobs, u, ok.ID)
		if g.Status != domain.JobDone || g.Saved != 5 || g.Error != "" || g.Attempts != 0 {
			t.Errorf("done job = %+v", g)
		}
		if g.Coverage == nil || g.Coverage.Events != 5 || g.Coverage.New != 4 || g.Coverage.PerType["reddit.post"] != 5 {
			t.Errorf("coverage round trip = %+v", g.Coverage)
		}
		if g := get(t, jobs, u, bad.ID); g.Status != domain.JobError || g.Error != "boom" || g.Coverage != nil || g.Attempts != 0 {
			t.Errorf("failed job = %+v (coverage %v)", g, g.Coverage)
		}
		if err := jobs.Complete(ctx, unknownID, domain.JobDone, 0, nil, ""); err != nil {
			t.Errorf("Complete(unknown) = %v, want a no-op", err)
		}
	})

	t.Run("Requeue takes finished jobs only and keeps the crash counter", func(t *testing.T) {
		jobs, env := factory(t)
		u := env.NewUser(t)
		j := newJob(t, jobs, u, 0)
		if _, ok, err := jobs.Requeue(ctx, u, j.ID); !ok || !errors.Is(err, app.ErrJobBusy) {
			t.Errorf("Requeue(queued) = ok %v, err %v; want true, ErrJobBusy", ok, err)
		}
		claim(t, jobs)
		if _, ok, err := jobs.Requeue(ctx, u, j.ID); !ok || !errors.Is(err, app.ErrJobBusy) {
			t.Errorf("Requeue(running) = ok %v, err %v; want true, ErrJobBusy", ok, err)
		}
		if g := get(t, jobs, u, j.ID); g.Status != domain.JobRunning {
			t.Errorf("a refused Requeue changed the status to %q", g.Status)
		}

		for _, outcome := range []domain.JobStatus{domain.JobDone, domain.JobError} {
			_ = jobs.Progress(ctx, j.ID, 3, 30, 60)
			if err := jobs.Complete(ctx, j.ID, outcome, 3, nil, "msg"); err != nil {
				t.Fatal(err)
			}
			rq, ok, err := jobs.Requeue(ctx, u, j.ID)
			if err != nil || !ok || rq.Status != domain.JobQueued || rq.Error != "" || rq.ID != j.ID {
				t.Fatalf("Requeue(%s) = %+v,%v,%v; want it queued with the error cleared", outcome, rq, ok, err)
			}
			again := claim(t, jobs)
			if again.ID != j.ID || again.Saved != 0 || again.BytesDone != 0 || again.BytesTotal != 0 {
				t.Errorf("re-claimed job = %+v; want the same job with its progress reset", again)
			}
		}

		// A run that crashes never reaches Complete. Requeue must not hand such
		// an archive a fresh budget: only a recorded outcome resets Attempts.
		_ = jobs.Complete(ctx, j.ID, domain.JobDone, 0, nil, "")
		_, _, _ = jobs.Requeue(ctx, u, j.ID)
		for crash := 1; crash <= 3; crash++ {
			claim(t, jobs)
			rq, failed, err := jobs.Recover(ctx, 3)
			if wantRequeue := crash < 3; err != nil || (rq == 1) != wantRequeue || (failed == 1) == wantRequeue {
				t.Fatalf("Recover after crash %d = %d requeued, %d failed, %v", crash, rq, failed, err)
			}
		}
		rq, ok, err := jobs.Requeue(ctx, u, j.ID)
		if err != nil || !ok || rq.Attempts != 3 {
			t.Fatalf("Requeue of a crasher = attempts %d, ok %v, err %v; want attempts kept at 3", rq.Attempts, ok, err)
		}
		claim(t, jobs)
		if _, failed, _ := jobs.Recover(ctx, 3); failed != 1 {
			t.Errorf("a reprocessed crasher got a fresh attempts budget (failed = %d, want 1)", failed)
		}
	})

	t.Run("Recover requeues orphans and gives up at max attempts", func(t *testing.T) {
		jobs, env := factory(t)
		u := env.NewUser(t)
		orphan := newJob(t, jobs, u, 0)
		finished := newJob(t, jobs, u, time.Minute)
		claim(t, jobs) // orphan: running, attempts 1
		claim(t, jobs)
		_ = jobs.Complete(ctx, finished.ID, domain.JobDone, 1, nil, "")
		waiting := newJob(t, jobs, u, 2*time.Minute)

		if rq, failed, err := jobs.Recover(ctx, 2); err != nil || rq != 1 || failed != 0 {
			t.Fatalf("Recover = %d requeued, %d failed, %v; want 1, 0", rq, failed, err)
		}
		if g := get(t, jobs, u, orphan.ID); g.Status != domain.JobQueued || g.Attempts != 1 {
			t.Errorf("recovered orphan = %s/attempts %d; want queued with attempts kept at 1", g.Status, g.Attempts)
		}
		if g := get(t, jobs, u, finished.ID); g.Status != domain.JobDone {
			t.Errorf("Recover touched a finished job: %s", g.Status)
		}
		if g := get(t, jobs, u, waiting.ID); g.Status != domain.JobQueued || g.Attempts != 0 {
			t.Errorf("Recover touched a queued job: %s/attempts %d", g.Status, g.Attempts)
		}
		if rq, failed, err := jobs.Recover(ctx, 2); err != nil || rq != 0 || failed != 0 {
			t.Errorf("Recover with nothing running = %d,%d,%v; want 0,0,nil", rq, failed, err)
		}

		if got := claim(t, jobs); got.ID != orphan.ID || got.Attempts != 2 {
			t.Fatalf("re-claim = %+v; want the orphan (oldest) with attempts 2", got)
		}
		if rq, failed, err := jobs.Recover(ctx, 2); err != nil || rq != 0 || failed != 1 {
			t.Fatalf("Recover at max attempts = %d requeued, %d failed, %v; want 0, 1", rq, failed, err)
		}
		g := get(t, jobs, u, orphan.ID)
		if g.Status != domain.JobError || g.Error != domain.JobGaveUpMessage {
			t.Errorf("exhausted job = %s / %q; want error / %q", g.Status, g.Error, domain.JobGaveUpMessage)
		}
		if !g.CanRequeue() {
			t.Error("a job Recover gave up on must be reprocessable by the user")
		}
	})

	t.Run("Delete refuses running jobs and cancels queued ones", func(t *testing.T) {
		jobs, env := factory(t)
		u := env.NewUser(t)
		running := newJob(t, jobs, u, 0)
		queued := newJob(t, jobs, u, time.Minute)
		claim(t, jobs)
		if out, err := jobs.Delete(ctx, u, running.ID); err != nil || out != app.JobBusy {
			t.Errorf("Delete(running) = %v,%v; want JobBusy", out, err)
		}
		if g := get(t, jobs, u, running.ID); g.Status != domain.JobRunning {
			t.Errorf("a refused Delete changed the job: %+v", g)
		}
		if out, err := jobs.Delete(ctx, u, queued.ID); err != nil || out != app.JobDeleted {
			t.Errorf("Delete(queued) = %v,%v; want JobDeleted", out, err)
		}
		if _, ok, _ := jobs.Claim(ctx); ok {
			t.Error("a deleted queued job was still claimed")
		}
		_ = jobs.Complete(ctx, running.ID, domain.JobError, 0, nil, "x")
		if out, err := jobs.Delete(ctx, u, running.ID); err != nil || out != app.JobDeleted {
			t.Errorf("Delete(finished) = %v,%v; want JobDeleted", out, err)
		}
		if out, err := jobs.Delete(ctx, u, running.ID); err != nil || out != app.JobNotFound {
			t.Errorf("Delete(deleted) = %v,%v; want JobNotFound", out, err)
		}
		if _, ok, _ := jobs.Get(ctx, u, running.ID); ok {
			t.Error("job still exists after Delete")
		}
	})

	t.Run("RequestCancel fails queued jobs, flags running ones, refuses finished ones", func(t *testing.T) {
		jobs, env := factory(t)
		u, other := env.NewUser(t), env.NewUser(t)
		running := newJob(t, jobs, u, 0)
		queued := newJob(t, jobs, u, time.Minute)
		claim(t, jobs) // running
		if got, ok, err := jobs.RequestCancel(ctx, other, running.ID); ok || err != nil || got.ID != "" {
			t.Errorf("RequestCancel(another user's job) = %+v,%v,%v; want not found", got, ok, err)
		}
		for _, id := range []string{unknownID, malformedID, ""} {
			if _, ok, err := jobs.RequestCancel(ctx, u, id); ok || err != nil {
				t.Errorf("RequestCancel(%q) = %v,%v; want not found", id, ok, err)
			}
		}

		got, ok, err := jobs.RequestCancel(ctx, u, queued.ID)
		if err != nil || !ok || got.Status != domain.JobError || got.Error != domain.JobCancelledMessage || got.CancelRequested {
			t.Errorf("RequestCancel(queued) = %+v,%v,%v; want an error job with the cancel message", got, ok, err)
		}
		if _, ok, _ := jobs.Claim(ctx); ok {
			t.Error("a cancelled queued job was still claimed")
		}

		got, ok, err = jobs.RequestCancel(ctx, u, running.ID)
		if err != nil || !ok || got.Status != domain.JobRunning || !got.CancelRequested {
			t.Errorf("RequestCancel(running) = %+v,%v,%v; want running with CancelRequested", got, ok, err)
		}
		if g := get(t, jobs, u, running.ID); !g.CancelRequested {
			t.Error("Get does not report CancelRequested")
		}
		if _, ok, _ := jobs.Get(ctx, other, running.ID); ok {
			t.Error("another user can see the job")
		}
	})

	t.Run("CancelRequested is cleared by Complete, Requeue and Claim; Recover fails such a job", func(t *testing.T) {
		jobs, env := factory(t)
		u := env.NewUser(t)
		j := newJob(t, jobs, u, 0)
		claim(t, jobs)
		if _, _, err := jobs.RequestCancel(ctx, u, j.ID); err != nil {
			t.Fatal(err)
		}
		if _, _, err := jobs.RequestCancel(ctx, u, j.ID); err != nil {
			t.Errorf("a second RequestCancel on a running job = %v; want a no-op", err)
		}
		if rq, failed, err := jobs.Recover(ctx, 5); err != nil || rq != 0 || failed != 1 {
			t.Fatalf("Recover with a cancel-requested orphan = %d requeued, %d failed, %v; want 0, 1", rq, failed, err)
		}
		g := get(t, jobs, u, j.ID)
		if g.Status != domain.JobError || g.Error != domain.JobCancelledMessage || g.CancelRequested || g.Attempts != 0 {
			t.Errorf("recovered cancelled job = %+v; want error, cancel message, flag cleared, attempts 0", g)
		}
		if got, ok, err := jobs.RequestCancel(ctx, u, j.ID); !ok || !errors.Is(err, app.ErrJobFinished) || got.ID != j.ID {
			t.Errorf("RequestCancel(finished) = %+v,%v,%v; want the job and ErrJobFinished", got, ok, err)
		}

		if _, _, err := jobs.Requeue(ctx, u, j.ID); err != nil {
			t.Fatal(err)
		}
		claim(t, jobs)
		_, _, _ = jobs.RequestCancel(ctx, u, j.ID)
		if err := jobs.Complete(ctx, j.ID, domain.JobDone, 1, nil, ""); err != nil {
			t.Fatal(err)
		}
		if g := get(t, jobs, u, j.ID); g.CancelRequested {
			t.Error("Complete kept CancelRequested")
		}
		if g, _, _ := jobs.Requeue(ctx, u, j.ID); g.CancelRequested || g.Status != domain.JobQueued {
			t.Errorf("Requeue = %+v; want queued without CancelRequested", g)
		}
		if g := claim(t, jobs); g.CancelRequested {
			t.Error("Claim handed out a job with CancelRequested")
		}
	})

	t.Run("Stats counts queued and running jobs of all users", func(t *testing.T) {
		jobs, env := factory(t)
		if st, err := jobs.Stats(ctx); err != nil || st != (app.JobStats{}) {
			t.Fatalf("Stats of an empty queue = %+v, %v", st, err)
		}
		u, other := env.NewUser(t), env.NewUser(t)
		newJob(t, jobs, u, 0)
		newJob(t, jobs, u, time.Minute)
		done := newJob(t, jobs, other, 2*time.Minute)
		claim(t, jobs) // u's first: running
		if st, err := jobs.Stats(ctx); err != nil || st != (app.JobStats{Queued: 2, Running: 1}) {
			t.Errorf("Stats = %+v, %v; want 2 queued, 1 running", st, err)
		}
		_ = jobs.Complete(ctx, done.ID, domain.JobDone, 0, nil, "")
		if st, _ := jobs.Stats(ctx); st != (app.JobStats{Queued: 1, Running: 1}) {
			t.Errorf("Stats after completing a job = %+v; want 1 queued, 1 running", st)
		}
	})

	t.Run("blob ledger: paths map to content keys, each key counted once", func(t *testing.T) {
		jobs, env := factory(t)
		u, other := env.NewUser(t), env.NewUser(t)
		a := newJob(t, jobs, u, 0)
		b := newJob(t, jobs, u, time.Minute)
		o := newJob(t, jobs, other, 2*time.Minute)
		base, err := jobs.Usage(ctx, u)
		if err != nil || base.BlobBytes != 0 {
			t.Fatalf("Usage before recording = %+v, %v", base, err)
		}
		record := func(user, job string, blobs ...app.JobBlob) []string {
			t.Helper()
			orphaned, err := jobs.RecordBlobs(ctx, user, job, blobs)
			if err != nil || orphaned == nil {
				t.Fatalf("RecordBlobs = %v, %v", orphaned, err)
			}
			return orphaned
		}
		// a: two names for one file, and a file of its own.
		record(u, a.ID,
			app.JobBlob{Path: "s/IMG-1.jpg", Key: "s/sha256/aaa", Bytes: 10},
			app.JobBlob{Path: "s/copy-of-IMG-1.jpg", Key: "s/sha256/aaa", Bytes: 10},
			app.JobBlob{Path: "s/only-a.jpg", Key: "s/sha256/ccc", Bytes: 5})
		record(u, a.ID, app.JobBlob{Path: "s/only-a.jpg", Key: "s/sha256/ccc", Bytes: 5}) // again: no-op
		// b: the same name as a's first file, but other content — and a's file.
		record(u, b.ID,
			app.JobBlob{Path: "s/IMG-1.jpg", Key: "s/sha256/bbb", Bytes: 7},
			app.JobBlob{Path: "s/other-name.jpg", Key: "s/sha256/aaa", Bytes: 10})
		record(other, o.ID, app.JobBlob{Path: "s/IMG-1.jpg", Key: "s/sha256/zzz", Bytes: 1000})
		// Someone else's job records nothing.
		record(other, a.ID, app.JobBlob{Path: "s/sneaky.jpg", Key: "s/sha256/sneaky", Bytes: 1 << 40})
		record(u, unknownID, app.JobBlob{Path: "x", Key: "x", Bytes: 1})
		record(u, malformedID, app.JobBlob{Path: "x", Key: "x", Bytes: 1})

		if use, err := jobs.Usage(ctx, u); err != nil || use.BlobBytes != 22 || use.Stored() != use.ArchiveBytes+22 {
			t.Errorf("Usage = %+v, %v; want BlobBytes 22 (aaa, bbb, ccc once each)", use, err)
		}
		// The same path resolves per import: each export's own file.
		for _, c := range []struct{ user, job, path, want string }{
			{u, a.ID, "s/IMG-1.jpg", "s/sha256/aaa"},
			{u, b.ID, "s/IMG-1.jpg", "s/sha256/bbb"},
			{u, a.ID, "s/copy-of-IMG-1.jpg", "s/sha256/aaa"},
			{u, b.ID, "s/only-a.jpg", "s/sha256/ccc"}, // not b's: the most recent job that has it
			{u, "", "s/IMG-1.jpg", "s/sha256/bbb"},    // no job: the most recent one (b)
			{other, o.ID, "s/IMG-1.jpg", "s/sha256/zzz"},
		} {
			if got, ok, err := jobs.MediaKey(ctx, c.user, c.job, c.path); err != nil || !ok || got != c.want {
				t.Errorf("MediaKey(%s) = %q, %v, %v; want %q", c.path, got, ok, err, c.want)
			}
		}
		for _, c := range []struct{ user, job, path string }{
			{u, a.ID, "s/never.jpg"}, {other, o.ID, "s/only-a.jpg"}, {u, unknownID, "s/nope"}, {malformedID, a.ID, "s/IMG-1.jpg"},
		} {
			if got, ok, err := jobs.MediaKey(ctx, c.user, c.job, c.path); err != nil || ok {
				t.Errorf("MediaKey(%q, %q) = %q, %v, %v; want not found", c.user, c.path, got, ok, err)
			}
		}

		if got, err := jobs.ExclusiveBlobs(ctx, u, a.ID); err != nil || !slices.Equal(got, []string{"s/sha256/ccc"}) {
			t.Errorf("ExclusiveBlobs(a) = %v, %v; want [s/sha256/ccc] (aaa is b's too)", got, err)
		}
		if got, err := jobs.ExclusiveBlobs(ctx, other, a.ID); err != nil || got == nil || len(got) != 0 {
			t.Errorf("ExclusiveBlobs(a) as another user = %#v, %v; want empty", got, err)
		}
		for _, id := range []string{unknownID, malformedID} {
			if got, err := jobs.ExclusiveBlobs(ctx, u, id); err != nil || got == nil || len(got) != 0 {
				t.Errorf("ExclusiveBlobs(%q) = %#v, %v; want empty", id, got, err)
			}
		}

		// The ledger goes with the job: b's keys are a's alone now if a has them.
		_ = jobs.Complete(ctx, b.ID, domain.JobDone, 0, nil, "")
		if out, err := jobs.Delete(ctx, u, b.ID); err != nil || out != app.JobDeleted {
			t.Fatalf("Delete(b) = %v, %v", out, err)
		}
		if got, _ := jobs.ExclusiveBlobs(ctx, u, a.ID); !slices.Equal(got, []string{"s/sha256/aaa", "s/sha256/ccc"}) {
			t.Errorf("after deleting b, ExclusiveBlobs(a) = %v", got)
		}
		if use, _ := jobs.Usage(ctx, u); use.BlobBytes != 15 {
			t.Errorf("after deleting b, BlobBytes = %d, want 15", use.BlobBytes)
		}
		if got, ok, _ := jobs.MediaKey(ctx, u, b.ID, "s/IMG-1.jpg"); !ok || got != "s/sha256/aaa" {
			t.Errorf("MediaKey for a deleted job's path = %q, %v; want the remaining job's key", got, ok)
		}
	})

	t.Run("recording a path under a new key reports the old key once nothing refers to it", func(t *testing.T) {
		jobs, env := factory(t)
		u := env.NewUser(t)
		a := newJob(t, jobs, u, 0)
		b := newJob(t, jobs, u, time.Minute)
		// Recorded before content addressing: key = path.
		if _, err := jobs.RecordBlobs(ctx, u, a.ID, []app.JobBlob{{Path: "s/x.jpg", Key: "s/x.jpg", Bytes: 3}, {Path: "s/y.jpg", Key: "s/y.jpg", Bytes: 4}}); err != nil {
			t.Fatal(err)
		}
		if _, err := jobs.RecordBlobs(ctx, u, b.ID, []app.JobBlob{{Path: "s/y.jpg", Key: "s/y.jpg", Bytes: 4}}); err != nil {
			t.Fatal(err)
		}
		// a's reprocess moves both to content keys: x's old blob is free, y's
		// is still b's.
		orphaned, err := jobs.RecordBlobs(ctx, u, a.ID, []app.JobBlob{
			{Path: "s/x.jpg", Key: "s/sha256/x", Bytes: 3}, {Path: "s/y.jpg", Key: "s/sha256/y", Bytes: 4},
		})
		if err != nil || !slices.Equal(orphaned, []string{"s/x.jpg"}) {
			t.Errorf("RecordBlobs (remap) = %v, %v; want [s/x.jpg]", orphaned, err)
		}
		if got, _, _ := jobs.MediaKey(ctx, u, a.ID, "s/x.jpg"); got != "s/sha256/x" {
			t.Errorf("MediaKey after remap = %q", got)
		}
		if orphaned, _ := jobs.RecordBlobs(ctx, u, a.ID, []app.JobBlob{{Path: "s/x.jpg", Key: "s/sha256/x", Bytes: 3}}); len(orphaned) != 0 {
			t.Errorf("recording the same key again orphaned %v", orphaned)
		}
	})

	t.Run("ReleaseLegacyMedia: a job created now never stored media by path", func(t *testing.T) {
		jobs, env := factory(t)
		u, other := env.NewUser(t), env.NewUser(t)
		job := newJob(t, jobs, u, 0)
		if _, err := jobs.RecordBlobs(ctx, u, job.ID, []app.JobBlob{{Path: "s/x.jpg", Key: "s/sha256/x", Bytes: 1}}); err != nil {
			t.Fatal(err)
		}
		_ = jobs.Complete(ctx, job.ID, domain.JobDone, 0, nil, "")
		for _, c := range []struct{ user, job string }{{u, job.ID}, {other, job.ID}, {u, unknownID}, {u, malformedID}, {malformedID, job.ID}} {
			if got, err := jobs.ReleaseLegacyMedia(ctx, c.user, c.job); err != nil || got == nil || len(got) != 0 {
				t.Errorf("ReleaseLegacyMedia(%q, %q) = %#v, %v; want empty", c.user, c.job, got, err)
			}
		}
	})

	t.Run("CreateWithin counts stored blobs against the storage quota", func(t *testing.T) {
		jobs, env := factory(t)
		u := env.NewUser(t)
		first := newJob(t, jobs, u, 0) // 100 archive bytes
		_ = jobs.Complete(ctx, first.ID, domain.JobDone, 0, nil, "")
		if _, err := jobs.RecordBlobs(ctx, u, first.ID, []app.JobBlob{{Path: "s/big.mp4", Key: "s/sha256/big", Bytes: 800}}); err != nil {
			t.Fatal(err)
		}
		limits := app.Limits{MaxArchiveBytes: 1000}
		next := domain.ImportJob{UserID: u, Source: "reddit", ZipPaths: []string{"r"}, ArchiveBytes: 101}
		if _, err := jobs.CreateWithin(ctx, next, limits); !errors.Is(err, app.ErrStorageQuota) {
			t.Errorf("100 + 800 stored + 101 new over a 1000 quota = %v; want ErrStorageQuota", err)
		}
		next.ArchiveBytes = 100
		if _, err := jobs.CreateWithin(ctx, next, limits); err != nil {
			t.Errorf("exactly at the quota = %v", err)
		}
	})

	t.Run("Usage counts active jobs and all archive bytes", func(t *testing.T) {
		jobs, env := factory(t)
		u := env.NewUser(t)
		if use, err := jobs.Usage(ctx, u); err != nil || use != (app.JobUsage{}) {
			t.Errorf("Usage without jobs = %+v,%v; want zero", use, err)
		}
		a := newJob(t, jobs, u, 0)
		newJob(t, jobs, u, time.Minute)
		newJob(t, jobs, u, 2*time.Minute)
		claim(t, jobs) // a: running
		if use, _ := jobs.Usage(ctx, u); use.Active != 3 || use.ArchiveBytes != 300 {
			t.Errorf("Usage = %+v; want 3 active (queued + running), 300 bytes", use)
		}
		_ = jobs.Complete(ctx, a.ID, domain.JobDone, 0, nil, "")
		if use, _ := jobs.Usage(ctx, u); use.Active != 2 || use.ArchiveBytes != 300 {
			t.Errorf("Usage = %+v; want 2 active, and a finished job's archive still counted (300)", use)
		}
		_, _ = jobs.Delete(ctx, u, a.ID)
		if use, _ := jobs.Usage(ctx, u); use.Active != 2 || use.ArchiveBytes != 200 {
			t.Errorf("Usage after Delete = %+v; want 2 active, 200 bytes", use)
		}
	})
}

func jobIDs(jobs []domain.ImportJob) []string {
	ids := make([]string, len(jobs))
	for i, j := range jobs {
		ids[i] = j.ID
	}
	return ids
}
