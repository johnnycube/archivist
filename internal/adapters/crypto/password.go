// Package crypto implements the app's PasswordHasher port with Argon2id
// (golang.org/x/crypto/argon2). Hashes with weaker parameters than the current
// ones still verify and report NeedsRehash, so a login upgrades them.
package crypto

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"strings"

	"github.com/johnnycube/archivist/internal/app"

	"golang.org/x/crypto/argon2"
)

// Current parameters: OWASP Password Storage Cheat Sheet, Argon2id minimum
// configuration (m=19 MiB, t=2, p=1).
const (
	argonMemoryKiB = 19 * 1024 // 19456
	argonTime      = 2
	argonThreads   = 1
	argonKeyLen    = 32
	saltLen        = 16
)

// Upper bounds accepted when verifying a stored hash. Stored hashes are
// trusted data, but a crafted or corrupted row must not be able to make a
// login allocate gigabytes or spin for minutes.
const (
	maxArgonMemoryKiB = 256 * 1024 // 256 MiB
	maxArgonTime      = 16
	maxArgonThreads   = 16
	minKeyLen         = 16
	maxKeyLen         = 64
	minSaltLen        = 8
	maxSaltLen        = 64
)

const (
	argonPrefix = "argon2id"
)

// argonSlots bounds concurrent Argon2 computations: each one holds its full
// memory cost, so unbounded parallel logins would multiply 19 MiB by the
// number of in-flight requests.
var argonSlots = make(chan struct{}, max(2, runtime.GOMAXPROCS(0)))

// Hasher implements app.PasswordHasher. New hashes are Argon2id in the standard
// PHC string format.
type Hasher struct{}

var _ app.PasswordHasher = Hasher{}

// Hash returns a PHC string: $argon2id$v=19$m=19456,t=2,p=1$<salt>$<hash>
// (base64, raw standard alphabet, no padding).
func (Hasher) Hash(password string) (string, error) {
	if password == "" {
		return "", errors.New("empty password")
	}
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argonKey(password, salt, argonTime, argonMemoryKiB, argonThreads, argonKeyLen)
	return fmt.Sprintf("$%s$v=%d$m=%d,t=%d,p=%d$%s$%s", argonPrefix, argon2.Version,
		argonMemoryKiB, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// Verify checks password against the encoded hash in constant time. Malformed
// hashes and hashes with out-of-range parameters never verify.
func (Hasher) Verify(encoded, password string) bool {
	p, ok := parseArgon(encoded)
	if !ok {
		return false
	}
	keyLen := uint32(len(p.key)) //nolint:gosec // parseArgon bounds the key to maxKeyLen (64) bytes
	got := argonKey(password, p.salt, p.time, p.memoryKiB, p.threads, keyLen)
	return subtle.ConstantTimeCompare(got, p.key) == 1
}

// NeedsRehash reports whether a stored hash should be replaced with a fresh
// Hash of the (just verified) password: Argon2id hashes weaker than the
// current parameters, and anything unrecognized. Call it after
// a successful Verify, while the plaintext is still at hand.
func (Hasher) NeedsRehash(encoded string) bool {
	p, ok := parseArgon(encoded)
	if !ok {
		return true
	}
	return p.memoryKiB < argonMemoryKiB || p.time < argonTime || len(p.key) < argonKeyLen
}

func argonKey(password string, salt []byte, time, memoryKiB uint32, threads uint8, keyLen uint32) []byte {
	argonSlots <- struct{}{}
	defer func() { <-argonSlots }()
	return argon2.IDKey([]byte(password), salt, time, memoryKiB, threads, keyLen)
}

type argonParams struct {
	memoryKiB, time uint32
	threads         uint8
	salt, key       []byte
}

// parseArgon parses $argon2id$v=19$m=…,t=…,p=…$salt$hash and bounds-checks
// every parameter.
func parseArgon(encoded string) (argonParams, bool) {
	var p argonParams
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != argonPrefix {
		return p, false
	}
	if parts[2] != "v="+strconv.Itoa(argon2.Version) {
		return p, false
	}
	kv := strings.Split(parts[3], ",")
	if len(kv) != 3 {
		return p, false
	}
	m, okM := paramUint(kv[0], "m=", maxArgonMemoryKiB)
	t, okT := paramUint(kv[1], "t=", maxArgonTime)
	par, okP := paramUint(kv[2], "p=", maxArgonThreads)
	if !okM || !okT || !okP || m < 8*par { // Argon2 requires m ≥ 8·p
		return p, false
	}
	salt, key, ok := decodeSaltKey(parts[4], parts[5])
	if !ok {
		return p, false
	}
	return argonParams{memoryKiB: m, time: t, threads: uint8(par), salt: salt, key: key}, true
}

// paramUint parses "<prefix><decimal>" into [1, max].
func paramUint(s, prefix string, max uint32) (uint32, bool) {
	s, ok := strings.CutPrefix(s, prefix)
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseUint(s, 10, 32)
	if err != nil || n < 1 || n > uint64(max) {
		return 0, false
	}
	return uint32(n), true
}

func decodeSaltKey(saltB64, keyB64 string) (salt, key []byte, ok bool) {
	// Bound the encoded lengths before decoding anything.
	if len(saltB64) > 2*maxSaltLen || len(keyB64) > 2*maxKeyLen {
		return nil, nil, false
	}
	salt, err := base64.RawStdEncoding.DecodeString(saltB64)
	if err != nil || len(salt) < minSaltLen || len(salt) > maxSaltLen {
		return nil, nil, false
	}
	key, err = base64.RawStdEncoding.DecodeString(keyB64)
	if err != nil || len(key) < minKeyLen || len(key) > maxKeyLen {
		return nil, nil, false
	}
	return salt, key, true
}
