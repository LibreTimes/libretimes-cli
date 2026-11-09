package api

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
)

// CitationQuery picks what to cite: a work, optionally one edition of it,
// optionally one tome of that edition.
type CitationQuery struct {
	// Expression is an edition's id. Empty cites the edition the reader would
	// be shown (Lang decides which).
	Expression string
	// Volume is a tome's position within that edition. Needs Expression.
	Volume *int
	Lang   string
}

// Citation is the CSL-JSON of a work as the server builds it. CSL is kept raw:
// it is CSL 1.0.2's schema, not ours, and every citation tool reads it as is.
type Citation struct {
	WorkID       string          `json:"work_id"`
	ExpressionID string          `json:"expression_id,omitempty"`
	Volume       *int            `json:"volume,omitempty"`
	CSL          json.RawMessage `json:"csl"`
}

// GetCitation reads GET /works/{id}/citation. Public works need no sign-in.
func (c *Client) GetCitation(ctx context.Context, workID string, q CitationQuery) (*Citation, error) {
	query := url.Values{}
	if q.Expression != "" {
		query.Set("expression", q.Expression)
	}
	if q.Volume != nil {
		query.Set("volume", strconv.Itoa(*q.Volume))
	}
	if q.Lang != "" {
		query.Set("lang", q.Lang)
	}
	var out Citation
	if err := c.Get(ctx, "/works/"+url.PathEscape(workID)+"/citation", query, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
