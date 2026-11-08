package crypto

import (
	"strings"
	"testing"
)

const (
	// Independent vectors for the password "password", salt "somesaltsomesalt":
	//   openssl kdf -keylen 32 -kdfopt pass:password -kdfopt salt:somesaltsomesalt \
	//     -kdfopt iter:2 -kdfopt memcost:19456 -kdfopt lanes:1 -binary ARGON2ID | base64
	// weakVector: the same with memcost:4096 — verifies, but needs a rehash.
	argonVector = "$argon2id$v=19$m=19456,t=2,p=1$c29tZXNhbHRzb21lc2FsdA$K13EBUiG7JV+9ZxztmHFTdb7J0WQsnj2V8bZaqyPptE"
	weakVector  = "$argon2id$v=19$m=4096,t=2,p=1$c29tZXNhbHRzb21lc2FsdA$H5mZn8QsFFwntPktdbjGNtqB+DDJ+nixq94uJAZoqIk"
)

func TestHasher(t *testing.T) {
	h := Hasher{}
	enc, err := h.Hash("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(enc, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("hash = %q, want an argon2id PHC string with OWASP parameters", enc)
	}
	if strings.Contains(enc, "=") && strings.HasSuffix(enc, "=") {
		t.Errorf("hash %q is base64-padded", enc)
	}
	if !h.Verify(enc, "correct horse battery staple") {
		t.Error("valid password rejected")
	}
	if h.Verify(enc, "wrong") {
		t.Error("wrong password accepted")
	}
	if h.NeedsRehash(enc) {
		t.Error("a fresh hash must not need a rehash")
	}
	enc2, _ := h.Hash("correct horse battery staple")
	if enc == enc2 {
		t.Error("expected distinct salts")
	}
	if _, err := h.Hash(""); err == nil {
		t.Error("empty password must be rejected")
	}
}

func TestVerifyKnownVectors(t *testing.T) {
	h := Hasher{}
	for name, enc := range map[string]string{"argon2id": argonVector, "weaker argon2id": weakVector} {
		if !h.Verify(enc, "password") {
			t.Errorf("%s: known-good vector rejected", name)
		}
		if h.Verify(enc, "Password") {
			t.Errorf("%s: wrong password accepted", name)
		}
	}
}

func TestNeedsRehash(t *testing.T) {
	h := Hasher{}
	salt, key := "c29tZXNhbHRzb21lc2FsdA", "K13EBUiG7JV+9ZxztmHFTdb7J0WQsnj2V8bZaqyPptE"
	cases := map[string]bool{
		argonVector: false,
		weakVector:  true,
		"$argon2id$v=19$m=65536,t=3,p=4$" + salt + "$" + key:      false, // stronger: keep
		"$argon2id$v=19$m=4096,t=2,p=1$" + salt + "$" + key:       true,  // less memory
		"$argon2id$v=19$m=19456,t=1,p=1$" + salt + "$" + key:      true,  // fewer passes
		"$argon2id$v=19$m=19456,t=2,p=1$" + salt + "$" + key[:22]: true,  // 16-byte tag
		"":        true,
		"garbage": true,
	}
	for enc, want := range cases {
		if got := h.NeedsRehash(enc); got != want {
			t.Errorf("NeedsRehash(%q) = %v, want %v", enc, got, want)
		}
	}
}

// TestVerifyRejectsHostileHashes: malformed or absurdly expensive encodings
// must be refused up front, not computed.
func TestVerifyRejectsHostileHashes(t *testing.T) {
	h := Hasher{}
	salt, key := "c29tZXNhbHRzb21lc2FsdA", "K13EBUiG7JV+9ZxztmHFTdb7J0WQsnj2V8bZaqyPptE"
	for _, enc := range []string{
		"",
		"$",
		"plaintext",
		"$argon2id$v=19$m=19456,t=2,p=1$" + salt, // missing hash
		"$argon2id$v=16$m=19456,t=2,p=1$" + salt + "$" + key,          // unsupported version
		"$argon2i$v=19$m=19456,t=2,p=1$" + salt + "$" + key,           // other variant
		"$argon2id$v=19$m=4294967295,t=2,p=1$" + salt + "$" + key,     // 4 TiB of memory
		"$argon2id$v=19$m=99999999999999,t=2,p=1$" + salt + "$" + key, // overflow
		"$argon2id$v=19$m=19456,t=4000000000,p=1$" + salt + "$" + key, // endless passes
		"$argon2id$v=19$m=19456,t=2,p=255$" + salt + "$" + key,        // thread bomb
		"$argon2id$v=19$m=19456,t=2,p=0$" + salt + "$" + key,          // zero lanes (panics in argon2)
		"$argon2id$v=19$m=0,t=2,p=1$" + salt + "$" + key,              //
		"$argon2id$v=19$m=8,t=2,p=4$" + salt + "$" + key,              // m < 8p
		"$argon2id$v=19$t=2,m=19456,p=1$" + salt + "$" + key,          // wrong order
		"$argon2id$v=19$m=-1,t=2,p=1$" + salt + "$" + key,             //
		"$argon2id$v=19$m=19456,t=2,p=1$!!!$" + key,                   // bad base64
		"$argon2id$v=19$m=19456,t=2,p=1$" + salt + "$",                // empty hash
		"$argon2id$v=19$m=19456,t=2,p=1$YQ$" + key,                    // 1-byte salt
		"$argon2id$v=19$m=19456,t=2,p=1$" + salt + "$" + strings.Repeat("A", 4096),
		// PBKDF2 hashes from before Argon2id are no longer accepted.
		"pbkdf2_sha256$1000$c29tZXNhbHRzb21lc2FsdA$s5LQUeAEZUMuFVrnmF3OMNPXs3QWnF8SO/5BXmCj6QQ",
	} {
		if h.Verify(enc, "password") {
			t.Errorf("Verify(%q) = true", enc)
		}
	}
}
