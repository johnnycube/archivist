// Package domain holds the enterprise entities and the contracts for the things
// the archive is about — Events, Users, and the notion of a data Source. It has
// no dependencies on frameworks, databases, transport, or any adapter: the
// dependency rule points inward, so everything else imports domain and domain
// imports nothing of ours.
package domain

import "time"

// Category is the coarse, cross-source facet used for filtering the timeline.
type Category string

const (
	CategoryContent     Category = "content"
	CategoryMessage     Category = "message"
	CategoryReaction    Category = "reaction"
	CategorySave        Category = "save"
	CategoryConnection  Category = "connection"
	CategoryMedia       Category = "media"
	CategoryProfile     Category = "profile"
	CategoryConsumption Category = "consumption"
	CategoryOther       Category = "other"
)

// Valid reports whether c is one of the categories above — the closed set the
// timeline can be filtered by.
func (c Category) Valid() bool {
	switch c {
	case CategoryContent, CategoryMessage, CategoryReaction, CategorySave, CategoryConnection,
		CategoryMedia, CategoryProfile, CategoryConsumption, CategoryOther:
		return true
	}
	return false
}

// Relation describes how an Event acts on its Target.
type Relation string

const (
	RelationLiked     Relation = "liked"
	RelationVotedUp   Relation = "voted_up"
	RelationVotedDown Relation = "voted_down"
	RelationSaved     Relation = "saved"
	RelationFollowed  Relation = "followed"
	RelationRepliedTo Relation = "replied_to"
	RelationShared    Relation = "shared"
)

// Target is what an Event acted upon (the liked tweet, the saved link, …).
type Target struct {
	Relation         Relation
	TargetSource     string
	TargetExternalID string
	TargetURL        string
}

// Media is an attachment associated with an Event.
type Media struct {
	BlobKey string
	MIME    string
	URL     string
}

// Event is the canonical, normalized unit of everything on the timeline.
type Event struct {
	SourceID   string
	ExternalID string
	Type       string
	Category   Category

	OccurredAt time.Time
	Actor      string

	Title string
	Body  string

	ThreadExternalID string
	ParentExternalID string
	Targets          []Target
	Media            []Media

	Metadata map[string]any
}

// TypeDef declares the semantics of a namespaced Event type.
type TypeDef struct {
	Type        string
	Category    Category
	HasBody     bool
	NeedsTarget bool
}

// TypeRegistry is an extensible vocabulary of Event types.
type TypeRegistry struct{ defs map[string]TypeDef }

func NewTypeRegistry() *TypeRegistry { return &TypeRegistry{defs: map[string]TypeDef{}} }

func (r *TypeRegistry) Register(defs ...TypeDef) {
	for _, d := range defs {
		r.defs[d.Type] = d
	}
}

func (r *TypeRegistry) Lookup(t string) (TypeDef, bool) {
	d, ok := r.defs[t]
	return d, ok
}
