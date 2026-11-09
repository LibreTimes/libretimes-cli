package api

import (
	"context"
	"encoding/json"
	"net/url"
)

// Edit sessions (the platform's EDIT-SESSIONS-PLAN-2026-10-07): a working copy
// of a work that takes exact {old, new} replacements -- and, for a book,
// structural ops -- and becomes one version on commit. Readers see nothing
// until then, and a commit over a field someone changed meanwhile is refused
// (409, "STALE_SESSION").
//
// The change and op objects are passed through as the file wrote them
// (json.RawMessage): the API validates them and names what it refuses, so a
// second copy of the shape here could only drift from it.

// EditSession is the subset of a session lt reports.
type EditSession struct {
	ID        string `json:"id"`
	WorkID    string `json:"work_id"`
	Status    string `json:"status"`
	ExpiresAt string `json:"expires_at"`
}

// EditResult is one change's or op's outcome.
type EditResult struct {
	Index      int    `json:"index"`
	OK         bool   `json:"ok"`
	Error      string `json:"error,omitempty"`
	DivisionID string `json:"division_id,omitempty"`
}

// EditDiff is what a session would change.
type EditDiff struct {
	Targets []struct {
		Language   string `json:"language,omitempty"`
		DivisionID string `json:"division_id,omitempty"`
		Field      string `json:"field"`
		Diff       string `json:"diff"`
	} `json:"targets"`
	Structure []string `json:"structure"`
}

// EditCommitted is a commit's answer.
type EditCommitted struct {
	VersionNumber   *int `json:"version_number"`
	AwaitingPublish bool `json:"awaiting_publish"`
}

func editsPath(workID string) string {
	return "/works/" + url.PathEscape(workID) + "/edits"
}

func sessionPath(workID, sessionID string) string {
	return editsPath(workID) + "/" + url.PathEscape(sessionID)
}

// OpenEdit opens a session on the work; client names the opener in its record.
func (c *Client) OpenEdit(ctx context.Context, workID, client string) (*EditSession, error) {
	var out EditSession
	if err := c.Post(ctx, editsPath(workID), map[string]string{"client": client}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ApplyStructure records a book's create/move/delete ops, all or none.
func (c *Client) ApplyStructure(ctx context.Context, workID, sessionID string, ops []json.RawMessage) ([]EditResult, error) {
	var out struct {
		Results []EditResult `json:"results"`
	}
	if err := c.Post(ctx, sessionPath(workID, sessionID)+"/structure", map[string]any{"ops": ops}, &out); err != nil {
		return nil, err
	}
	return out.Results, nil
}

// ApplyChanges applies exact replacements in order, all or none.
func (c *Client) ApplyChanges(ctx context.Context, workID, sessionID string, changes []json.RawMessage) ([]EditResult, error) {
	var out struct {
		Results []EditResult `json:"results"`
	}
	if err := c.Post(ctx, sessionPath(workID, sessionID)+"/changes", map[string]any{"changes": changes}, &out); err != nil {
		return nil, err
	}
	return out.Results, nil
}

// DiffEdit reads what the session would change.
func (c *Client) DiffEdit(ctx context.Context, workID, sessionID string) (*EditDiff, error) {
	var out EditDiff
	if err := c.Get(ctx, sessionPath(workID, sessionID)+"/diff", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CommitEdit makes one version of everything the session changed.
func (c *Client) CommitEdit(ctx context.Context, workID, sessionID, summary string) (*EditCommitted, error) {
	var out EditCommitted
	if err := c.Post(ctx, sessionPath(workID, sessionID)+"/commit", map[string]string{"summary": summary}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DiscardEdit throws the session away.
func (c *Client) DiscardEdit(ctx context.Context, workID, sessionID string) error {
	return c.Delete(ctx, sessionPath(workID, sessionID))
}
