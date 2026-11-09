package api

import "context"

// RecordResult is what POST /works/records answers: the record, and whether
// this call made it. Created false means the catalogue already held the work
// (MatchedBy names the identifier) and that record came back unchanged.
type RecordResult struct {
	Work      Work   `json:"work"`
	Created   bool   `json:"created"`
	MatchedBy string `json:"matched_by,omitempty"`
}

// RecordWork adds a work the caller did not write -- a book, say, for a
// reading list. The body is the public API's own shape, sent as the file has
// it: the server validates and names the field it refuses.
func (c *Client) RecordWork(ctx context.Context, body map[string]any) (*RecordResult, error) {
	var out RecordResult
	if err := c.Post(ctx, "/works/records", body, &out); err != nil {
		return nil, err
	}
	out.Work.normalize()
	return &out, nil
}
