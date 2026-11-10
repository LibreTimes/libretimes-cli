package api

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
)

// CitationStyle is one style the server formats in (REFERENCES-PLAN R9). The
// list is the server's: IEEE, APA, Chicago author-date, GOST R 7.0.5-2008.
type CitationStyle struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Kind    string `json:"kind"`
	Default bool   `json:"default"`
}

// StyledEntry is one formatted entry. Runs carries the marks (italic, small
// caps, the cited title) for a renderer; Text is the entry as plain text.
type StyledEntry struct {
	ID   string          `json:"id"`
	Runs json.RawMessage `json:"runs"`
	Text string          `json:"text"`
}

// Bibliography is a list formatted in one style, with one language's terms.
type Bibliography struct {
	Style   string        `json:"style"`
	Lang    string        `json:"lang"`
	Entries []StyledEntry `json:"entries"`
}

// BibliographyQuery picks a work's list: a version of it (0 = the one
// served), the terms' language and a style (empty = the work's own).
type BibliographyQuery struct {
	Version int
	Lang    string
	Style   string
}

// ListCitationStyles reads GET /citation-styles. Public.
func (c *Client) ListCitationStyles(ctx context.Context) ([]CitationStyle, error) {
	var out []CitationStyle
	if err := c.Get(ctx, "/citation-styles", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetBibliography reads GET /works/{id}/bibliography: what a work cites,
// formatted on the server. As readable as the work itself.
func (c *Client) GetBibliography(ctx context.Context, workID string, q BibliographyQuery) (*Bibliography, error) {
	query := url.Values{}
	if q.Version > 0 {
		query.Set("version", strconv.Itoa(q.Version))
	}
	if q.Lang != "" {
		query.Set("lang", q.Lang)
	}
	if q.Style != "" {
		query.Set("style", q.Style)
	}
	var out Bibliography
	if err := c.Get(ctx, "/works/"+url.PathEscape(workID)+"/bibliography", query, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// FormatCitations posts CSL-JSON items to POST /citations/format and returns
// them in one style. Public; the server rate-limits it.
func (c *Client) FormatCitations(ctx context.Context, items []json.RawMessage, style, lang string) (*Bibliography, error) {
	body := map[string]any{"items": items, "style": style}
	if lang != "" {
		body["lang"] = lang
	}
	var out Bibliography
	if err := c.Post(ctx, "/citations/format", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
