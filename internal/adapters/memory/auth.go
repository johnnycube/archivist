package memory

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/johnnycube/archivist/internal/app"
	"github.com/johnnycube/archivist/internal/domain"

	"github.com/google/uuid"
)

// newID mirrors Postgres' gen_random_uuid(): a random (v4) UUID.
func newID() string { return uuid.NewString() }

// Users is an in-memory app.Users.
type Users struct {
	mu         sync.RWMutex
	byEmail    map[string]domain.User
	byID       map[string]domain.User
	identities map[identityKey]string // external identity → user id
	// onDelete is what a user's deletion cascades to (set by NewStore; a
	// stand-alone Users has nothing to cascade to).
	onDelete []func(userID string)
}

type identityKey struct{ provider, subject string }

func NewUsers() *Users {
	return &Users{byEmail: map[string]domain.User{}, byID: map[string]domain.User{}, identities: map[identityKey]string{}}
}

var _ app.Users = (*Users)(nil)

func (m *Users) Create(_ context.Context, email, hash string) (domain.User, error) {
	email = domain.NormalizeEmail(email)
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.byEmail[email]; ok {
		return domain.User{}, app.ErrEmailTaken
	}
	u := domain.User{ID: newID(), Email: email, PasswordHash: hash}
	m.byEmail[email] = u
	m.byID[u.ID] = u
	return u, nil
}

func (m *Users) ByEmail(_ context.Context, email string) (domain.User, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	u, ok := m.byEmail[domain.NormalizeEmail(email)]
	return u, ok, nil
}

func (m *Users) ByID(_ context.Context, id string) (domain.User, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	u, ok := m.byID[id]
	return u, ok, nil
}

func (m *Users) UpdatePassword(_ context.Context, id, hash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.byID[id]
	if !ok {
		return nil
	}
	u.PasswordHash = hash
	m.byID[id] = u
	m.byEmail[u.Email] = u
	return nil
}

// Delete removes the user and, in a Store, everything they own — the
// equivalent of PostgreSQL's ON DELETE CASCADE.
func (m *Users) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	u, ok := m.byID[id]
	if ok {
		delete(m.byEmail, u.Email)
		delete(m.byID, id)
		for k, owner := range m.identities {
			if owner == id {
				delete(m.identities, k)
			}
		}
	}
	cascade := m.onDelete
	m.mu.Unlock()
	if ok {
		for _, f := range cascade {
			f(id)
		}
	}
	return nil
}

// ByIdentity finds the user an external identity is linked to.
func (m *Users) ByIdentity(_ context.Context, provider, subject string) (domain.User, bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	id, ok := m.identities[identityKey{provider, subject}]
	if !ok {
		return domain.User{}, false, nil
	}
	u, ok := m.byID[id]
	return u, ok, nil
}

// LinkIdentity attaches an external identity to a user (see app.Users).
func (m *Users) LinkIdentity(_ context.Context, userID, provider, subject, _ string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.byID[userID]; !ok {
		return fmt.Errorf("link identity to user %q: %w", userID, app.ErrNotFound)
	}
	key := identityKey{provider, subject}
	if owner, ok := m.identities[key]; ok && owner != userID {
		return app.ErrIdentityTaken
	}
	m.identities[key] = userID
	return nil
}

// LinkedProviders lists the providers the user has an identity at.
func (m *Users) LinkedProviders(_ context.Context, userID string) ([]string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	seen := map[string]bool{}
	out := []string{}
	for k, owner := range m.identities {
		if owner == userID && !seen[k.provider] {
			seen[k.provider] = true
			out = append(out, k.provider)
		}
	}
	slices.Sort(out)
	return out, nil
}

// List returns every user, ordered by email.
func (m *Users) List(context.Context) ([]domain.User, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]domain.User, 0, len(m.byEmail))
	for _, u := range m.byEmail {
		out = append(out, u)
	}
	slices.SortFunc(out, func(a, b domain.User) int { return strings.Compare(a.Email, b.Email) })
	return out, nil
}

// MarkEmailVerified records that a provider vouched for the user's email.
func (m *Users) MarkEmailVerified(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.byID[id]
	if !ok {
		return nil
	}
	u.EmailVerified = true
	m.byID[id] = u
	m.byEmail[u.Email] = u
	return nil
}

// Accounts is an in-memory app.Accounts.
type Accounts struct {
	mu sync.Mutex
	m  map[accountKey]string
}

type accountKey struct{ userID, source, label string }

func NewAccounts() *Accounts { return &Accounts{m: map[accountKey]string{}} }

var _ app.Accounts = (*Accounts)(nil)

func (a *Accounts) Ensure(_ context.Context, userID, source, label string) (string, error) {
	if label == "" {
		label = "default"
	}
	key := accountKey{userID, source, label}
	a.mu.Lock()
	defer a.mu.Unlock()
	if id, ok := a.m[key]; ok {
		return id, nil
	}
	id := newID()
	a.m[key] = id
	return id, nil
}

func (a *Accounts) deleteUser(userID string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for k := range a.m {
		if k.userID == userID {
			delete(a.m, k)
		}
	}
}
