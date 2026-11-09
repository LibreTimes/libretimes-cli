package api

import (
	"context"
	"net/url"
	"strconv"
)

// CategoryHit is one category a term found (TAXONOMY-INPUT T6): its label in
// the reader's language, its context in the taxonomy, and what matched.
//
// Context is the nearest domain over related edges -- `Algebra · Mathematics`
// (a path to one domain) or `Chemistry, Biology` (two equally near) -- and is
// empty for a domain. The taxonomy is a graph, so it is never a breadcrumb.
type CategoryHit struct {
	ID          string   `json:"id"`
	Key         string   `json:"key"`
	Level       string   `json:"level,omitempty"`
	Name        string   `json:"name"`
	Language    string   `json:"language,omitempty"`
	Context     string   `json:"context,omitempty"`
	ContextKeys []string `json:"context_keys,omitempty"`
	// Match is key | name | alias | translation | stem | typo | layout.
	Match       string `json:"match"`
	MatchedText string `json:"matched_text,omitempty"`
}

// Label is the hit as a person reads it: `Linear algebra — Algebra · Mathematics`.
func (h CategoryHit) Label() string {
	if h.Context == "" {
		return h.Name
	}
	return h.Name + " — " + h.Context
}

// TermResolution is what one term is, decided exactly as a save decides it:
// one category, or a keyword (Category nil) with loose suggestions.
type TermResolution struct {
	Term        string        `json:"term"`
	Text        string        `json:"text"`
	Norm        string        `json:"norm"`
	Category    *CategoryHit  `json:"category"`
	Generic     bool          `json:"generic"`
	Suggestions []CategoryHit `json:"suggestions"`
}

// KeyResolution is one category key: a category, or none with suggestions.
type KeyResolution struct {
	Key         string        `json:"key"`
	Category    *CategoryHit  `json:"category"`
	Suggestions []CategoryHit `json:"suggestions"`
}

// ResolveTermsInput is the body of the public resolve-terms read.
type ResolveTermsInput struct {
	Lang        string   `json:"lang,omitempty"`
	Terms       []string `json:"terms"`
	Keys        []string `json:"keys"`
	Suggestions int      `json:"suggestions"`
}

// ResolveTermsOutput is its answer.
type ResolveTermsOutput struct {
	Terms []TermResolution `json:"terms"`
	Keys  []KeyResolution  `json:"keys"`
}

// SuggestCategories is the loose read an editor calls as an author types:
// prefix, stem, typo and keyboard layout. It only offers; nothing is saved.
// Public -- the taxonomy is -- so it works with no token.
func (c *Client) SuggestCategories(ctx context.Context, q, lang string, limit int) ([]CategoryHit, error) {
	query := url.Values{"q": {q}}
	if lang != "" {
		query.Set("lang", lang)
	}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	var out []CategoryHit
	if err := c.Get(ctx, "/categories/suggest", query, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// ResolveTerms classifies terms and keys exactly, the way a save will.
// Public, like SuggestCategories.
func (c *Client) ResolveTerms(ctx context.Context, in ResolveTermsInput) (*ResolveTermsOutput, error) {
	if in.Terms == nil {
		in.Terms = []string{}
	}
	if in.Keys == nil {
		in.Keys = []string{}
	}
	var out ResolveTermsOutput
	if err := c.Post(ctx, "/categories/resolve-terms", in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
