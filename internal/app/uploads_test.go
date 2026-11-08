package app_test

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/johnnycube/archivist/internal/app"
)

// diskStaging is a staging area with room for `free` bytes; it records the
// size every CheckSpace asked about.
type diskStaging struct {
	app.Staging
	free  int64
	asked []int64
}

func (d *diskStaging) CheckSpace(_ context.Context, size int64) error {
	d.asked = append(d.asked, size)
	if size > d.free {
		return app.ErrInsufficientStorage
	}
	return nil
}

func (d *diskStaging) Put(ctx context.Context, userID string, r io.Reader) (string, int64, error) {
	return d.Staging.Put(ctx, userID, r)
}

// Uploads in flight count before they are jobs: against the user's import
// slots and storage quota, and against the free staging space for everyone.
func TestAdmitReservesUploadsInFlight(t *testing.T) {
	ctx := context.Background()
	deps := importDeps(t, nil)
	disk := &diskStaging{Staging: deps.Staging, free: 1000}
	deps.Staging = disk
	deps.Limits = app.Limits{MaxActiveImports: 2, MaxArchiveBytes: 700}
	a := app.New(deps)

	r1, err := a.Import.Admit(ctx, "u1", "reddit", 300)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := a.Import.Admit(ctx, "u1", "reddit", 300)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Import.Admit(ctx, "u1", "reddit", 1); !errors.Is(err, app.ErrTooManyImports) {
		t.Errorf("third upload in flight = %v; want ErrTooManyImports", err)
	}
	r2()
	r2() // release is idempotent
	if _, err := a.Import.Admit(ctx, "u1", "reddit", 401); !errors.Is(err, app.ErrStorageQuota) {
		t.Errorf("300 in flight + 401 over a 700 quota = %v; want ErrStorageQuota", err)
	}
	// Another user: their own quota, but the same disk.
	if _, err := a.Import.Admit(ctx, "u2", "reddit", 701); !errors.Is(err, app.ErrInsufficientStorage) && !errors.Is(err, app.ErrStorageQuota) {
		t.Errorf("u2 over disk and quota = %v", err)
	}
	u2, err := a.Import.Admit(ctx, "u2", "reddit", 699)
	if err != nil {
		t.Errorf("300 reserved + 699 on a disk of 1000 = %v; want it to fit", err)
	}
	if got := disk.asked[len(disk.asked)-1]; got != 999 {
		t.Errorf("CheckSpace asked for %d; want the reserved 300 plus the new 699", got)
	}
	if _, err := a.Import.Admit(ctx, "u3", "reddit", 2); !errors.Is(err, app.ErrInsufficientStorage) {
		t.Errorf("999 reserved + 2 on a disk of 1000 = %v; want ErrInsufficientStorage", err)
	}
	u2()
	r1()
	if r, err := a.Import.Admit(ctx, "u1", "reddit", 700); err != nil {
		t.Errorf("after both releases, a full-quota upload = %v", err)
	} else {
		r()
	}
}
