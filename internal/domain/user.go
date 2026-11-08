package domain

import (
	"strings"
	"time"
)

// User is an account holder (a tenant).
type User struct {
	ID           string
	Email        string
	PasswordHash string
	// EmailVerified records that an identity provider vouched for Email (a
	// login or link whose ID token said email_verified for this address).
	// Password signup proves nothing about the address, so it starts false.
	// Only a verified account may be found by email for an OIDC login: an
	// unverified one could have been registered by anyone under that address.
	EmailVerified bool
}

// NormalizeEmail is THE identity rule for emails: surrounding whitespace is
// ignored and case does not matter, so "Me@Example.com " and "me@example.com"
// are the same user. Stores persist and look up the normalized form.
func NormalizeEmail(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

// StoredEvent is an Event with storage identity and tenancy attached.
type StoredEvent struct {
	ID        string
	UserID    string
	AccountID string
	// ImportJobID is the import that last wrote this event ("" = none, e.g. seed).
	ImportJobID string
	IngestedAt  time.Time
	Event
}
