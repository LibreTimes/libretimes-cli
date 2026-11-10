package api

import (
	"context"
	"net/url"
)

// WorkCardRef is the part of a work card a resolution needs.
type WorkCardRef struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// WorkResolution is what POST /works/resolve found. Only Indexed and
// Suggestions are used by lt: `resolved` is a record the platform has not
// stored, and lt never creates a work from it.
type WorkResolution struct {
	Kind        string        `json:"kind"`
	Indexed     *WorkCardRef  `json:"indexed"`
	Suggestions []WorkCardRef `json:"suggestions"`
}

// ResolveWork resolves a DOI, arXiv id, ISBN or title against works the
// platform already holds. Nothing is persisted by this call.
func (c *Client) ResolveWork(ctx context.Context, query string) (*WorkResolution, error) {
	var out WorkResolution
	if err := c.Post(ctx, "/works/resolve", map[string]string{"query": query}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Expression is one edition of a work.
type Expression struct {
	ID       string   `json:"id"`
	Label    string   `json:"label,omitempty"`
	Title    string   `json:"title,omitempty"`
	ISBN13   string   `json:"isbn13,omitempty"`
	Language string   `json:"language,omitempty"`
	Year     int      `json:"year,omitempty"`
	Volumes  []Volume `json:"volumes,omitempty"`
}

// Volume is one tome of an edition printed in several, with its own ISBN.
type Volume struct {
	Label  string `json:"label,omitempty"`
	ISBN13 string `json:"isbn13,omitempty"`
}

// WorkKeyRef is a library work found by its catalogue key.
type WorkKeyRef struct {
	ID        string `json:"id"`
	ImportKey string `json:"import_key"`
	Title     string `json:"title"`
	Type      string `json:"type"`
}

// FindWorkByKey looks up a library work by the key its catalogue gave it
// (`book:<key>`) or an alias of it. Returns (nil, nil) when no public work
// holds the key -- or when the API predates the route, which reads the same.
func (c *Client) FindWorkByKey(ctx context.Context, key string) (*WorkKeyRef, error) {
	var out WorkKeyRef
	if err := c.Get(ctx, "/works/by-key", url.Values{"key": {key}}, &out); err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	return &out, nil
}

// ListExpressions reads a work's editions.
func (c *Client) ListExpressions(ctx context.Context, workID string) ([]Expression, error) {
	var out []Expression
	if err := c.Get(ctx, "/works/"+url.PathEscape(workID)+"/expressions", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}
