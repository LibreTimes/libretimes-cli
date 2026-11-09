package api

import (
	"context"
	"net/url"
	"reflect"
	"strconv"
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
//
// The Go names are the frontmatter's; the JSON names are `/works`': a file's
// `type` is a work's `kind`, its `tags` are `keywords`.
type PublicationInput struct {
	// Origin is set by CreatePublication: everything lt writes is hosted.
	Origin      string   `json:"origin,omitempty"`
	Title       string   `json:"title,omitempty"`
	Content     string   `json:"content,omitempty"`
	Description string   `json:"description,omitempty"`
	Type        string   `json:"kind,omitempty"`
	License     string   `json:"license,omitempty"`
	Language    string   `json:"language,omitempty"`
	Place       string   `json:"place,omitempty"`
	Visibility  string   `json:"visibility,omitempty"`
	AIUsage     string   `json:"ai_usage,omitempty"`
	IsDraft     *bool    `json:"is_draft,omitempty"`
	Tags        []string `json:"keywords,omitempty"`
	CategoryIDs []string `json:"category_ids,omitempty"`
	// IsDraft is create-only on the API: an update changes the lifecycle
	// through /publish and /unpublish, which UpdatePublication calls for it.
	//
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
	ID         string `json:"id"`
	Title      string `json:"title"`
	IsDraft    bool   `json:"is_draft"`
	Visibility string `json:"visibility,omitempty"`
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
// /works/me/by-import-key reports the caller's removed and draft rows
// precisely because they are the rows whose keys still occupy the index.
//
// Returns (nil, nil) when the caller has never imported under this key. That
// is the only 404 this path can produce — it is owner-scoped, so it means "you
// have not imported this", not "you may not look".
func (c *Client) FindPublicationByImportKey(ctx context.Context, key string) (*PublicationRef, error) {
	var ref PublicationRef
	err := c.Get(ctx, "/works/me/by-import-key", url.Values{"key": {key}}, &ref)
	if err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &ref, nil
}

// CreatePublication posts a new hosted work.
func (c *Client) CreatePublication(ctx context.Context, in PublicationInput) (*Publication, error) {
	in.Origin = "hosted"
	var out Publication
	if err := c.Post(ctx, "/works", in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdatePublication patches an existing work by id, then moves it into or out
// of draft if `IsDraft` asks for a state it is not in.
//
// Field edits go first, so a publish sees them. The publish then states the
// visibility -- the file's, else the one the work already has -- because the
// API's publish always applies one and defaults it to `public`: a bodyless
// publish made every `visibility: private` file public (2026-10-07). A file
// that says `draft: false` means "publish", not "and make it public".
func (c *Client) UpdatePublication(ctx context.Context, id string, in PublicationInput) (*Publication, error) {
	// The key is immutable and addressing is by id here; sending it would be
	// at best ignored and at worst a 422. The byline is not part of this body
	// either: it has its own routes.
	in.Origin = ""
	in.ImportKey = ""
	in.Authors = nil
	in.CreatorRole = ""
	wantDraft := in.IsDraft
	in.IsDraft = nil

	path := workPath(id)
	var out Publication
	var err error
	if reflect.ValueOf(in).IsZero() {
		err = c.Get(ctx, path, nil, &out)
	} else {
		err = c.Patch(ctx, path, in, &out)
	}
	if err != nil {
		return nil, err
	}
	if wantDraft != nil && *wantDraft != out.IsDraft {
		verb, body := "/publish", any(nil)
		if *wantDraft {
			verb = "/unpublish"
		} else if visibility := firstNonEmpty(in.Visibility, out.Visibility); visibility != "" {
			body = map[string]string{"visibility": visibility}
		}
		out = Publication{}
		if err := c.Post(ctx, path+verb, body, &out); err != nil {
			return nil, err
		}
	}
	return &out, nil
}

// workPath is one work's route.
func workPath(id string) string {
	return "/works/" + url.PathEscape(id)
}

// removedStatuses are the record states in which a work is gone from view:
// what the API called `removed` before it had a status log.
func isRemoved(recordStatus string) bool {
	return recordStatus == "removed" || recordStatus == "taken_down"
}

// Work is a work as `lt work` reads it: enough to show it, to find its
// files, and to reconcile its attachments without disturbing its file face.
//
// `Type` is the kind (`lecture`, `book`), as a file's frontmatter names it.
// The API's own `type` is the world's vocabulary (`lecture-speech`), so it is
// not decoded; normalize fills Type and Removed from `kind` and
// `record_status`.
type Work struct {
	ID               string       `json:"id"`
	Title            string       `json:"title"`
	Type             string       `json:"type"`
	Kind             string       `json:"kind,omitempty"`
	RecordStatus     string       `json:"record_status,omitempty"`
	Language         string       `json:"language"`
	Visibility       string       `json:"visibility"`
	IsDraft          bool         `json:"is_draft"`
	Removed          bool         `json:"removed"`
	ImportKey        string       `json:"import_key,omitempty"`
	BodyFormat       string       `json:"body_format,omitempty"`
	BodyAttachmentID string       `json:"body_attachment_id,omitempty"`
	AttachmentIDs    []string     `json:"attachment_ids"`
	Attachments      []Attachment `json:"attachments"`
	UpdatedAt        string       `json:"updated_at,omitempty"`
}

// normalize puts the kind where a file reads its type, and derives Removed.
func (w *Work) normalize() {
	if w.Kind != "" {
		w.Type = w.Kind
	}
	w.Removed = isRemoved(w.RecordStatus)
}

// GetWork reads one work. The caller's own drafts are visible to it.
func (c *Client) GetWork(ctx context.Context, id string) (*Work, error) {
	var out Work
	if err := c.Get(ctx, workPath(id), nil, &out); err != nil {
		return nil, err
	}
	out.normalize()
	return &out, nil
}

// SetAttachmentIDs replaces a publication's list of bound files.
//
// The list is the whole binding: an id left out is unbound. Callers build it
// from the list they just read, so a file they did not mean to touch stays.
// Its own body type, not PublicationInput: an empty list is a real value here,
// and omitempty would turn "unbind the last file" into "change nothing".
func (c *Client) SetAttachmentIDs(ctx context.Context, id string, ids []string) error {
	if ids == nil {
		ids = []string{}
	}
	body := struct {
		AttachmentIDs []string `json:"attachment_ids"`
	}{ids}
	return c.Patch(ctx, workPath(id), body, nil)
}

// listPageLimit is the API's maximum page size for the caller's own lists.
const listPageLimit = 100

// ListMyWorks returns all of the caller's works -- published, unlisted and
// drafts -- most recently changed first: `/works/me` with no `state` is one
// list.
func (c *Client) ListMyWorks(ctx context.Context) ([]Work, error) {
	var all []Work
	for offset := 0; ; {
		var page struct {
			Items []Work `json:"items"`
		}
		query := url.Values{
			"limit":  {strconv.Itoa(listPageLimit)},
			"offset": {strconv.Itoa(offset)},
		}
		if err := c.Get(ctx, "/works/me", query, &page); err != nil {
			return nil, err
		}
		for i := range page.Items {
			page.Items[i].normalize()
		}
		all = append(all, page.Items...)
		offset += len(page.Items)
		if len(page.Items) < listPageLimit {
			break
		}
	}
	return all, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
