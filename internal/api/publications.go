package api

import (
	"context"
	"net/url"
)

// PublicationInput is the set of fields the write paths accept.
//
// Kept explicit rather than relaying an arbitrary map, so a misspelled
// frontmatter key becomes a visible "unknown field" rather than one the API
// silently drops. That failure mode has bitten before: `tags` went missing
// from the Python importer's payload and the loss was visible only on the
// rendered page.
//
// omitempty throughout, because the API distinguishes "not supplied" from
// "set to empty" on a PATCH — sending a zero value would clear a field the
// file never mentioned.
type PublicationInput struct {
	Title       string   `json:"title,omitempty"`
	Content     string   `json:"content,omitempty"`
	Description string   `json:"description,omitempty"`
	Type        string   `json:"type,omitempty"`
	License     string   `json:"license,omitempty"`
	Language    string   `json:"language,omitempty"`
	Place       string   `json:"place,omitempty"`
	Visibility  string   `json:"visibility,omitempty"`
	AIUsage     string   `json:"ai_usage,omitempty"`
	IsDraft     *bool    `json:"is_draft,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	CategoryIDs []string `json:"category_ids,omitempty"`
	// ImportKey is set only on create. On update the row is already addressed
	// by id, and the key is immutable.
	ImportKey string `json:"import_key,omitempty"`
	// Authors and CreatorRole are create-only too. The update body carries no
	// byline, so on a re-run the byline is reconciled route by route instead
	// (see ListAuthors and InviteAuthor).
	Authors     []AuthorInvite `json:"authors,omitempty"`
	CreatorRole string         `json:"creator_role,omitempty"`
}

// Permitted values, named once so help text and validation cannot drift.
//
// Visibility is the one worth spelling out: the platform has two visibility
// vocabularies and they are not interchangeable. VisibilityLevel
// (public/authenticated/connections/owner) governs profile field groups; this
// one governs a publication. Documenting the wrong set is how three of four
// suggested values came to be rejected by a 422 that did not say which field
// was wrong.
var (
	VisibilityValues = []string{"public", "private", "by_link"}
	AIUsageValues    = []string{"none", "assisted", "mostly_ai", "fully_ai"}
)

// PublicationRef is what the import-key lookup returns: enough to decide what
// to do, and deliberately not the publication body. A removed publication is
// content its author withdrew, so the route reports that the row exists and
// what state it is in rather than serving it back.
type PublicationRef struct {
	ID        string `json:"id"`
	ImportKey string `json:"import_key"`
	Title     string `json:"title"`
	IsDraft   bool   `json:"is_draft"`
	Removed   bool   `json:"removed"`
}

// Publication is the subset of a created/updated publication lt reports back.
// Open by construction: the API owns this shape and adding a field to it must
// not break this client.
type Publication struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	IsDraft bool   `json:"is_draft"`
}

// FindPublicationByImportKey looks up one of the caller's own publications by
// its import key, whatever state it is in.
//
// This is the lookup `lt publish` is built on, and the ordering it enables —
// find-then-update, never create-then-fallback-on-conflict — is load-bearing
// rather than stylistic. A publication is *soft*-deleted, and a removed row
// keeps both its import_key and its slot in the partial unique index. So for a
// removed row the create path 409s permanently and a fallback after the
// conflict is never reachable. Exactly the rows a fallback would need to
// handle are the rows it cannot reach.
//
// /publications/me/by-import-key reports the caller's removed and draft rows
// precisely because they are the rows whose keys still occupy the index.
//
// Returns (nil, nil) when the caller has never imported under this key. That
// is the only 404 this path can produce — it is owner-scoped, so it means "you
// have not imported this", not "you may not look".
func (c *Client) FindPublicationByImportKey(ctx context.Context, key string) (*PublicationRef, error) {
	var ref PublicationRef
	err := c.Get(ctx, "/publications/me/by-import-key", url.Values{"key": {key}}, &ref)
	if err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &ref, nil
}

// CreatePublication posts a new publication.
func (c *Client) CreatePublication(ctx context.Context, in PublicationInput) (*Publication, error) {
	var out Publication
	if err := c.Post(ctx, "/publications", in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdatePublication patches an existing publication by id.
func (c *Client) UpdatePublication(ctx context.Context, id string, in PublicationInput) (*Publication, error) {
	// The key is immutable and addressing is by id here; sending it would be
	// at best ignored and at worst a 422. The byline is not part of this body
	// either: it has its own routes.
	in.ImportKey = ""
	in.Authors = nil
	in.CreatorRole = ""
	var out Publication
	if err := c.Patch(ctx, "/publications/"+url.PathEscape(id), in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
