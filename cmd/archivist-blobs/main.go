// Command archivist-blobs moves an existing single-node install's blob store
// from the filesystem (DATA_DIR/blobs) to an S3-compatible object store.
//
//	BLOB_S3_ENDPOINT=http://127.0.0.1:9000 BLOB_S3_ACCESS_KEY=… BLOB_S3_SECRET_KEY=… \
//	  archivist-blobs migrate-to-s3 -from ./data/blobs
//
// The copy is idempotent and resumable: objects the bucket already holds are
// skipped (write-once on both sides), so an interrupted run is simply started
// again. Nothing is deleted from the source. The server may keep running on
// the filesystem store while this copies; once a re-run reports "copied 0",
// restart the server with the BLOB_S3_* variables set and archive the
// directory. The S3 settings are the server's own BLOB_S3_* variables, each
// overridable by a flag.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/johnnycube/archivist/internal/adapters/blob"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "archivist-blobs:", err)
		os.Exit(1)
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: archivist-blobs migrate-to-s3 [-from DIR] [-endpoint URL] [-bucket NAME] [-access-key K] [-secret-key S] [-region R] [-sse] [-quiet]")
	fmt.Fprintln(w, "  copies every blob under DIR (default $DATA_DIR/blobs, ./data/blobs) into the S3 store named by")
	fmt.Fprintln(w, "  BLOB_S3_ENDPOINT / BLOB_S3_BUCKET / BLOB_S3_ACCESS_KEY / BLOB_S3_SECRET_KEY / BLOB_S3_REGION / BLOB_S3_SSE")
	fmt.Fprintln(w, "  (the server's variables; flags override). Idempotent and resumable; the source is left untouched.")
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "migrate-to-s3" {
		usage(stderr)
		return fmt.Errorf("expected the migrate-to-s3 command")
	}
	env := func(key, def string) string {
		if v := os.Getenv(key); v != "" {
			return v
		}
		return def
	}
	fs := flag.NewFlagSet("migrate-to-s3", flag.ContinueOnError)
	fs.SetOutput(stderr)
	from := fs.String("from", filepath.Join(env("DATA_DIR", "./data"), "blobs"), "filesystem blob store to copy from")
	endpoint := fs.String("endpoint", os.Getenv("BLOB_S3_ENDPOINT"), "S3 endpoint URL")
	bucket := fs.String("bucket", env("BLOB_S3_BUCKET", "archivist"), "bucket (created if missing)")
	accessKey := fs.String("access-key", os.Getenv("BLOB_S3_ACCESS_KEY"), "access key")
	secretKey := fs.String("secret-key", os.Getenv("BLOB_S3_SECRET_KEY"), "secret key")
	region := fs.String("region", env("BLOB_S3_REGION", "us-east-1"), "region")
	sse := fs.Bool("sse", os.Getenv("BLOB_S3_SSE") == "1" || os.Getenv("BLOB_S3_SSE") == "true", "request SSE-S3 (AES256) on every upload")
	quiet := fs.Bool("quiet", false, "print the summary only")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	s3, err := blob.NewS3(blob.S3Config{
		Endpoint: *endpoint, Bucket: *bucket, AccessKey: *accessKey, SecretKey: *secretKey, Region: *region, SSE: *sse,
	})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := s3.Check(ctx); err != nil {
		return err
	}
	if err := s3.EnsureBucket(ctx); err != nil {
		return err
	}

	fmt.Fprintf(stdout, "copying %s -> %s bucket %q\n", *from, *endpoint, *bucket)
	start := time.Now()
	lastReport := start
	progress := func(p blob.MigrateProgress) {
		if *quiet {
			return
		}
		// One line per copied object, and a heartbeat while skipping through
		// an already migrated store.
		if p.Copied || time.Since(lastReport) >= 2*time.Second {
			lastReport = time.Now()
			fmt.Fprintf(stdout, "  %-7s %s (%s)  copied %d, skipped %d, %s so far\n",
				map[bool]string{true: "copied", false: "skipped"}[p.Copied], p.Object, humanBytes(p.Size),
				p.Stats.Copied, p.Stats.Skipped, humanBytes(p.Stats.Bytes))
		}
	}
	stats, err := blob.MigrateFS(ctx, blob.NewFS(*from), s3, progress)
	fmt.Fprintf(stdout, "done in %s: copied %d objects (%s), skipped %d already present, ignored %d non-blob files\n",
		time.Since(start).Round(time.Millisecond), stats.Copied, humanBytes(stats.Bytes), stats.Skipped, stats.Ignored)
	if err != nil {
		return fmt.Errorf("%w (re-run to resume; finished objects are skipped)", err)
	}
	if stats.Copied == 0 && stats.Skipped > 0 {
		fmt.Fprintln(stdout, "nothing left to copy: the bucket holds every blob — switch the server to BLOB_S3_* now")
	}
	return nil
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
