package api

import (
	"context"
	"net/url"
)

// ContributorRoleValues is how a byline entry contributed, named once so help
// text and validation cannot drift. Mirrors libretimes-bff's ContributorRole.
// `author` renders in the run-in byline; every other value is a credit after
// it ("Lectures by ..."). A note reconstructed from someone's lectures credits
// them as `lecturer` -- the note-taker is the author.
var ContributorRoleValues = []string{
	"author", "advisor", "editor", "translator", "curator", "lecturer",
}

// AccessCredited is the only access lt ever asks for. A credit from a file is
// attribution, not rights: granting someone edit or ownership over a
// publication is a person's decision in the editor, not an import's.
const AccessCredited = "credited"

// AuthorInvite is one name to put on a byline, as the public API takes it.
//
// There is deliberately no field saying whether the person has an account.
// libretimes-bff decides that itself, off profile-service: an account-less
// profile (a lecturer created for the course-notes pipeline) is credited at
// once, and anyone with an account gets a pending invitation to accept. A
// client able to assert "no account" could attribute work to a real person
// without their consent.
type AuthorInvite struct {
	ProfileID string `json:"profile_id"`
	Role      string `json:"role"`
	Access    string `json:"access"`
}

// BylineEntry is the subset of a byline row lt reconciles against. Status is
// kept because a declined row still occupies the byline: re-asking someone
// who said no is exactly what a re-run must not do.
type BylineEntry struct {
	ProfileID string `json:"profile_id"`
	Role      string `json:"role"`
	Status    string `json:"status"`
}

// ListAuthors returns a publication's full byline, pending and declined rows
// included. The API restricts it to people already on that byline.
func (c *Client) ListAuthors(ctx context.Context, publicationID string) ([]BylineEntry, error) {
	var out []BylineEntry
	path := "/publications/" + url.PathEscape(publicationID) + "/authors"
	if err := c.Get(ctx, path, nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// InviteAuthor adds one name to an existing publication's byline.
func (c *Client) InviteAuthor(ctx context.Context, publicationID string, in AuthorInvite) (*BylineEntry, error) {
	var out BylineEntry
	path := "/publications/" + url.PathEscape(publicationID) + "/authors"
	if err := c.Post(ctx, path, in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ProfileByHandle resolves a public handle to its profile.
//
// The handle goes to the API exactly as written and is never case-folded
// here. The platform owns the fold, and it is expected to widen; a local
// `strings.ToLower` would disagree with it the day it does. The API also
// resolves a renamed handle through its history, so an old handle still
// answers with the current profile.
func (c *Client) ProfileByHandle(ctx context.Context, handle string) (*Profile, error) {
	var p Profile
	if err := c.Get(ctx, "/profiles/"+url.PathEscape(handle), nil, &p); err != nil {
		return nil, err
	}
	return &p, nil
}
