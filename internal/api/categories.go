package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
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

// TermReport is what one term, key or dropped id of a save became
// (TAXONOMY-INPUT T9): the `terms` list a work or collection write answers
// with. Code is TAG_IS_CATEGORY (a term named a category), GENERIC_TAG (a
// keyword too general to help, kept), CATEGORY_NOT_FOUND (a key or id that is
// no category), or empty.
type TermReport struct {
	Term        string        `json:"term"`
	Field       string        `json:"field"`
	Became      string        `json:"became"`
	Code        string        `json:"code,omitempty"`
	Category    *CategoryHit  `json:"category,omitempty"`
	Suggestions []CategoryHit `json:"suggestions,omitempty"`
}

// Line is the report entry as a person reads it.
func (t TermReport) Line() string {
	switch t.Became {
	case "category":
		if t.Category != nil {
			return fmt.Sprintf("%q -> category %s (%s)", t.Term, t.Category.Key, t.Category.Label())
		}
	case "keyword":
		line := fmt.Sprintf("%q -> keyword", t.Term)
		if t.Code == "GENERIC_TAG" {
			line += " [GENERIC_TAG: too general to help a reader]"
		}
		if hint := suggestionKeys(t.Suggestions); hint != "" {
			line += "; did you mean " + hint + "?"
		}
		return line
	}
	line := fmt.Sprintf("%q (%s) -> %s", t.Term, t.Field, t.Became)
	if t.Code != "" {
		line += " [" + t.Code + "]"
	}
	return line
}

func suggestionKeys(hits []CategoryHit) string {
	keys := make([]string, 0, len(hits))
	for _, h := range hits {
		keys = append(keys, h.Key)
	}
	return strings.Join(keys, ", ")
}

// termsOf reads the `terms` a refused save names, from an error envelope's
// structured detail: `{"error": {"detail": {"error_code", "message", "terms"}}}`.
func termsOf(body []byte) []TermReport {
	var envelope struct {
		Error struct {
			Detail json.RawMessage `json:"detail"`
		} `json:"error"`
	}
	if json.Unmarshal(body, &envelope) != nil || len(envelope.Error.Detail) == 0 {
		return nil
	}
	var detail struct {
		Terms []TermReport `json:"terms"`
	}
	if json.Unmarshal(envelope.Error.Detail, &detail) != nil {
		return nil
	}
	return detail.Terms
}
