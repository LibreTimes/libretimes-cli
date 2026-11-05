package api

import (
	"context"
	"slices"
	"sync"
)

// WorkTypes is the platform's type registry, `GET /work-types`: every kind of
// work a person can write here, and what each one decides -- its family,
// whether its author may publish it or a venue must, and which fields it
// needs before it can be published.
//
// lt keeps no list of kinds of its own. The registry is the server's, and a
// second copy here would be a second definition that drifts the day a kind is
// added. Open by construction: a field the API adds is ignored, not an error.
type WorkTypes struct {
	Version       int            `json:"version"`
	Kinds         []KindType     `json:"kinds"`
	ExternalTypes []ExternalType `json:"external_types"`
}

// KindType is one kind of hosted work, as a file's frontmatter `type` names it.
type KindType struct {
	Kind string `json:"kind"`
	// Family is `page` or `scientific`. A published work never moves between
	// the two (409 FAMILY_CHANGE).
	Family     string `json:"family"`
	Strictness string `json:"strictness"`
	// Publish is `self` (the author publishes) or `venue` (it stays a draft
	// until a venue publishes it; 409 VENUE_REQUIRED).
	Publish   string   `json:"publish"`
	Direction string   `json:"direction"`
	CSLType   string   `json:"csl_type"`
	Lead      KindLead `json:"lead"`
	// Required are the field keys a publish refuses without (409
	// WORK_INCOMPLETE); Recommended are advice.
	Required    []string `json:"required"`
	Recommended []string `json:"recommended"`
	Significant []string `json:"significant"`
	Scholarly   bool     `json:"scholarly"`
}

// KindLead is what the short text above a work's body is called.
type KindLead struct {
	Label  string `json:"label"`
	OnPage bool   `json:"on_page"`
	Max    int    `json:"max"`
}

// ExternalType is a world type of a record described here but published
// elsewhere. lt never writes one; it is listed for completeness.
type ExternalType struct {
	Type        string   `json:"type"`
	Family      string   `json:"family"`
	Required    []string `json:"required"`
	Recommended []string `json:"recommended"`
}

// Publish values.
const (
	PublishSelf  = "self"
	PublishVenue = "venue"
)

// Kind returns the entry for one kind, or nil when the registry has none.
func (t *WorkTypes) Kind(kind string) *KindType {
	if t == nil {
		return nil
	}
	i := slices.IndexFunc(t.Kinds, func(k KindType) bool { return k.Kind == kind })
	if i < 0 {
		return nil
	}
	return &t.Kinds[i]
}

// KindNames lists the kinds in the registry's own order.
func (t *WorkTypes) KindNames() []string {
	if t == nil {
		return nil
	}
	names := make([]string, 0, len(t.Kinds))
	for _, k := range t.Kinds {
		names = append(names, k.Kind)
	}
	return names
}

// workTypesCache holds the registry for the life of one Client, which is one
// lt invocation: a run over forty files reads it once. The zero value is
// ready to use, so New needs to know nothing about it.
type workTypesCache struct {
	mu    sync.Mutex
	value *WorkTypes
}

// WorkTypes reads the type registry. Public: no token is needed.
//
// A successful read is cached on the client; a failure is not, so the next
// caller asks again rather than inheriting a transient error.
func (c *Client) WorkTypes(ctx context.Context) (*WorkTypes, error) {
	cache := &c.types
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.value != nil {
		return cache.value, nil
	}
	var out WorkTypes
	if err := c.Get(ctx, "/work-types", nil, &out); err != nil {
		return nil, err
	}
	cache.value = &out
	return cache.value, nil
}
