package blob

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"iter"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/johnnycube/archivist/internal/app"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/minio/minio-go/v7/pkg/encrypt"
)

// s3PartSize is both the single-PUT threshold and the multipart part size.
// S3 allows 10 000 parts per upload, so 16 MiB parts cap one object at
// ~156 GiB — far above the largest export — while bounding the per-upload
// buffers to two parts' worth of memory.
const s3PartSize = 16 << 20

// s3CheckTimeout bounds the connectivity check (Check) so a wrong endpoint
// fails the boot in seconds, not after the transport's own retries.
const s3CheckTimeout = 5 * time.Second

// S3 implements app.Blobs against any S3-compatible object store (MinIO, AWS
// S3, Ceph RGW, …) using the minio-go client. Write-once semantics mirror the
// FS adapter's contract: ingested artifacts are immutable.
//
// Object layout matches the FS adapter: <userID>/<hh>/<sha256(key)> — hex-only
// object keys sidestep every S3 key-encoding edge case, and MigrateFS can copy
// a filesystem store object by object without knowing the original keys.
//
// Integrity: the client is created with trailing-header support, so every
// upload — a single PUT as well as each part of a streaming multipart upload —
// carries a CRC32C checksum (x-amz-checksum-crc32c) the server verifies before
// accepting the bytes. MinIO and AWS S3 both support this.
type S3 struct {
	bucket   string
	region   string
	client   *minio.Client
	partSize int64
	sse      encrypt.ServerSide // nil = no server-side encryption header
}

// S3Config configures the object store connection. Endpoint is a full URL
// (e.g. http://minio:9000 or https://s3.eu-central-1.amazonaws.com); its
// scheme selects TLS. Requests use path-style addressing, which MinIO speaks
// natively, unless VirtualHostStyle is set.
type S3Config struct {
	Endpoint  string
	Bucket    string
	AccessKey string
	SecretKey string
	Region    string // default us-east-1 (MinIO's default)

	// SSE asks the store to encrypt every object at rest with a store-managed
	// key (SSE-S3, "x-amz-server-side-encryption: AES256"). AWS S3 does this
	// by default; a MinIO without a configured KMS rejects the header, which
	// is why it is opt-in.
	SSE bool

	// VirtualHostStyle switches to <bucket>.<host> addressing, which some
	// hosted providers require. The default (false) is path-style.
	VirtualHostStyle bool
	// Transport overrides the HTTP transport (custom CAs, proxies, tests).
	// Nil uses minio-go's default.
	Transport http.RoundTripper
}

// NewS3 validates the config and returns the adapter. It performs no I/O;
// call Check and EnsureBucket once at startup.
func NewS3(cfg S3Config) (*S3, error) {
	if cfg.Endpoint == "" || cfg.Bucket == "" || cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, fmt.Errorf("s3 blobs: endpoint, bucket, access key, and secret key are all required")
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	base, err := url.Parse(cfg.Endpoint)
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return nil, fmt.Errorf("s3 blobs: endpoint must be a full URL like http://minio:9000")
	}
	if base.Path != "" && base.Path != "/" {
		return nil, fmt.Errorf("s3 blobs: endpoint must not contain a path (got %q)", base.Path)
	}
	lookup := minio.BucketLookupPath
	if cfg.VirtualHostStyle {
		lookup = minio.BucketLookupDNS
	}
	client, err := minio.New(base.Host, &minio.Options{
		Creds:        credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure:       base.Scheme == "https",
		Region:       cfg.Region,
		BucketLookup: lookup,
		Transport:    cfg.Transport,
		// Checksums on uploads (see the S3 doc comment). Requires SigV4, which
		// the static credentials above are.
		TrailingHeaders: true,
	})
	if err != nil {
		return nil, fmt.Errorf("s3 blobs: %w", err)
	}
	s := &S3{
		bucket:   cfg.Bucket,
		region:   cfg.Region,
		client:   client,
		partSize: s3PartSize,
	}
	if cfg.SSE {
		s.sse = encrypt.NewSSE()
	}
	return s, nil
}

var _ app.Blobs = (*S3)(nil)

// Check verifies, within a short timeout, that the endpoint answers and the
// credentials are accepted: a HEAD on the bucket. The error names the likely
// cause (unreachable endpoint, rejected credentials) so a misconfigured
// deployment fails fast with a message an operator can act on. A bucket that
// does not exist yet is NOT an error here — EnsureBucket creates it.
func (s *S3) Check(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, s3CheckTimeout)
	defer cancel()
	_, err := s.client.BucketExists(ctx, s.bucket)
	return s.classify(err)
}

// classify turns a minio-go error into an operator-facing one.
func (s *S3) classify(err error) error {
	if err == nil {
		return nil
	}
	resp := minio.ToErrorResponse(err)
	switch resp.Code {
	case minio.InvalidAccessKeyID, minio.SignatureDoesNotMatch, minio.AccessDenied, "InvalidSecurity", "AuthorizationHeaderMalformed":
		return fmt.Errorf("s3 blobs: credentials rejected by %s (%s): check BLOB_S3_ACCESS_KEY / BLOB_S3_SECRET_KEY", s.client.EndpointURL().Host, resp.Code)
	case "":
		var netErr net.Error
		if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &netErr) {
			return fmt.Errorf("s3 blobs: endpoint %s unreachable: %w (check BLOB_S3_ENDPOINT)", s.client.EndpointURL(), err)
		}
	}
	return fmt.Errorf("s3 blobs: %s: %w", s.client.EndpointURL().Host, err)
}

// Healthy is a readiness probe: one HEAD on the bucket, bounded by
// s3CheckTimeout. Callers must rate-limit it (see cmd/server's readiness
// wiring) — it is a network call.
func (s *S3) Healthy(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, s3CheckTimeout)
	defer cancel()
	ok, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return s.classify(err)
	}
	if !ok {
		return fmt.Errorf("s3 blobs: bucket %q does not exist", s.bucket)
	}
	return nil
}

// EnsureBucket creates the bucket if it does not exist (idempotent).
func (s *S3) EnsureBucket(ctx context.Context) error {
	ok, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("check bucket %q: %w", s.bucket, s.classify(err))
	}
	if ok {
		return nil
	}
	err = s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{Region: s.region})
	if err != nil && minio.ToErrorResponse(err).Code != minio.BucketAlreadyOwnedByYou { // lost a startup race
		return fmt.Errorf("create bucket %q: %w", s.bucket, s.classify(err))
	}
	return nil
}

// Bucket is the configured bucket name.
func (s *S3) Bucket() string { return s.bucket }

// userPrefix is the namespace every object of one user lives under. The
// trailing slash matters: "user-1/" must never match "user-10/…".
func userPrefix(userID string) string { return sanitize(userID) + "/" }

func objectName(userID, key string) string {
	sum := sha256.Sum256([]byte(key))
	h := hex.EncodeToString(sum[:])
	return userPrefix(userID) + h[:2] + "/" + h
}

// putOptions are the options every upload uses: 16 MiB parts, CRC32C
// checksums, and SSE-S3 when configured.
//
// Checksum (not AutoChecksum) is set on purpose: with only the default,
// minio-go skips the CRC on a known-size single PUT over plain HTTP because
// the SigV4 streaming signature already covers every chunk with SHA-256.
// Forcing it keeps one verifiable checksum on every object whatever the
// transport, and it is what multipart parts carry anyway.
func (s *S3) putOptions() minio.PutObjectOptions {
	return minio.PutObjectOptions{
		PartSize:             uint64(s.partSize),
		Checksum:             minio.ChecksumCRC32C,
		ServerSideEncryption: s.sse,
	}
}

// Put stores the object once; an existing object is left untouched
// (write-once, per the app.Blobs contract). The body is streamed: objects up
// to one part are sent as a single PUT, anything larger as a multipart upload
// of unknown length — nothing is spooled to disk.
func (s *S3) Put(ctx context.Context, userID, key string, r io.Reader) error {
	name := objectName(userID, key)

	exists, err := s.exists(ctx, name)
	if err != nil {
		return fmt.Errorf("s3 blobs: stat %s: %w", key, err)
	}
	if exists {
		return nil // immutable: first import won
	}

	// Read ahead at most one part to learn whether the object is small. The
	// buffer grows with the data, so a 200 KiB photo costs ~200 KiB.
	var head bytes.Buffer
	n, err := io.CopyN(&head, r, s.partSize)
	if err != nil && err != io.EOF {
		return fmt.Errorf("s3 blobs: read %s: %w", key, err)
	}

	opts := s.putOptions()
	if err == io.EOF {
		// Whole object in hand: one atomic PUT. If-None-Match closes the
		// stat→put race on stores that support conditional writes.
		opts.SetMatchETagExcept("*")
		_, err = s.client.PutObject(ctx, s.bucket, name, bytes.NewReader(head.Bytes()), n, opts)
		if err != nil && minio.ToErrorResponse(err).Code == minio.PreconditionFailed {
			return nil // a concurrent writer won; same contract as above
		}
	} else {
		body := io.MultiReader(bytes.NewReader(head.Bytes()), r)
		_, err = s.client.PutObject(ctx, s.bucket, name, body, -1, opts)
	}
	if err != nil {
		return fmt.Errorf("s3 blobs: put %s: %w", key, err)
	}
	return nil
}

// exists reports whether the named object is stored (one HEAD).
func (s *S3) exists(ctx context.Context, name string) (bool, error) {
	_, err := s.client.StatObject(ctx, s.bucket, name, minio.StatObjectOptions{})
	if err == nil {
		return true, nil
	}
	if isNotFound(err) {
		return false, nil
	}
	return false, err
}

// Open returns the object as a reader that also seeks (io.ReadSeeker) and
// reads at offsets (io.ReaderAt): the *minio.Object fetches ranges lazily,
// so http.ServeContent can answer Range requests with one ranged GET and a
// sequential reader streams the object with one GET — nothing is downloaded
// ahead of the caller. The single round trip Open itself makes is a HEAD,
// which is what turns a missing key into fs.ErrNotExist and what the Seek
// to the end that ServeContent starts with is answered from.
func (s *S3) Open(ctx context.Context, userID, key string) (io.ReadCloser, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, objectName(userID, key), minio.GetObjectOptions{})
	if err != nil { // argument validation only; no request has been made yet
		return nil, fmt.Errorf("s3 blobs: get %s: %w", key, err)
	}
	if _, err := obj.Stat(); err != nil {
		obj.Close()
		if isNotFound(err) {
			return nil, fmt.Errorf("s3 blobs: %s: %w", key, fs.ErrNotExist)
		}
		return nil, fmt.Errorf("s3 blobs: get %s: %w", key, err)
	}
	return obj, nil
}

// Delete removes the object; a missing key is not an error.
func (s *S3) Delete(ctx context.Context, userID, key string) error {
	err := s.client.RemoveObject(ctx, s.bucket, objectName(userID, key), minio.RemoveObjectOptions{})
	if err != nil && !isNotFound(err) {
		return fmt.Errorf("s3 blobs: delete %s: %w", key, err)
	}
	return nil
}

// DeleteUser removes every object under the user's prefix (account deletion).
// A user without objects is not an error.
func (s *S3) DeleteUser(ctx context.Context, userID string) error {
	if userID == "" {
		return errors.New("s3 blobs: delete user: empty user id")
	}
	var listErr error
	var objects iter.Seq[minio.ObjectInfo] = func(yield func(minio.ObjectInfo) bool) {
		list := s.client.ListObjectsIter(ctx, s.bucket, minio.ListObjectsOptions{
			Prefix:    userPrefix(userID),
			Recursive: true,
		})
		for obj := range list {
			if obj.Err != nil {
				listErr = obj.Err
				return
			}
			if !yield(obj) {
				return
			}
		}
	}
	results, err := s.client.RemoveObjectsWithIter(ctx, s.bucket, objects, minio.RemoveObjectsOptions{})
	if err != nil {
		return fmt.Errorf("s3 blobs: delete user: %w", err)
	}
	var errs []error
	for res := range results {
		if res.Err != nil && len(errs) < 5 { // keep draining; report a sample
			errs = append(errs, fmt.Errorf("%s: %w", res.ObjectName, res.Err))
		}
	}
	if listErr != nil {
		errs = append(errs, fmt.Errorf("list: %w", listErr))
	}
	if err := ctx.Err(); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return fmt.Errorf("s3 blobs: delete user: %w", errors.Join(errs...))
	}
	return nil
}

func isNotFound(err error) bool {
	// minio-go maps a body-less HEAD 404 on an object to NoSuchKey as well.
	return minio.ToErrorResponse(err).Code == minio.NoSuchKey
}
