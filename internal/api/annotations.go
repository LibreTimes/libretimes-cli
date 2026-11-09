package api

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"
)

// Annotations (the platform's EDITOR-REVIEW-PLAN-2026-10-17): notes, passage
// comments, suggested edits and errata on a work. Each is anchored to a
// passage by quoting the work's markdown -- the text lt reads and writes --
// with a little context around it, the same anchor the web page and the
// editor make, so a suggestion sent from here is highlighted on the page.
// Nothing is ever written into the text: a suggestion changes it only when
// the work's authors accept it.

// AnchorTarget is the text a passage is in: a single document's face
// (`language`, field `content`) or a book's division (field `body`).
type AnchorTarget struct {
	Language   string `json:"language,omitempty"`
	DivisionID string `json:"division_id,omitempty"`
	Field      string `json:"field"`
}

// AnchorQuote is the passage and the context either side of it.
type AnchorQuote struct {
	Exact  string `json:"exact"`
	Prefix string `json:"prefix"`
	Suffix string `json:"suffix"`
}

// AnchorHint is where the passage was, in UTF-16 code units -- the offsets a
// browser counts in. A tie-breaker only: the quote is what finds it.
type AnchorHint struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// Anchor addresses a passage.
type Anchor struct {
	Target  AnchorTarget `json:"target"`
	Version *int         `json:"version,omitempty"`
	Quote   AnchorQuote  `json:"quote"`
	Hint    *AnchorHint  `json:"hint,omitempty"`
}

// SuggestedChange is an exact replacement; `old` occurs once in the text.
type SuggestedChange struct {
	Old string `json:"old"`
	New string `json:"new"`
}

// Annotation is the subset of an annotation lt reports.
type Annotation struct {
	ID         string           `json:"id"`
	Kind       string           `json:"kind"`
	Visibility string           `json:"visibility"`
	Status     string           `json:"status,omitempty"`
	Body       string           `json:"body,omitempty"`
	Anchor     *Anchor          `json:"anchor,omitempty"`
	Change     *SuggestedChange `json:"change,omitempty"`
	Replies    int              `json:"replies_count"`
	IsDeleted  bool             `json:"is_deleted"`
	CreatedAt  string           `json:"created_at,omitempty"`
	Author     *struct {
		Username string `json:"username"`
	} `json:"author,omitempty"`
}

// AnnotationCreate is a new annotation.
type AnnotationCreate struct {
	Kind       string           `json:"kind"`
	Anchor     Anchor           `json:"anchor"`
	Visibility string           `json:"visibility,omitempty"`
	Change     *SuggestedChange `json:"change,omitempty"`
	Body       string           `json:"body,omitempty"`
}

// ResolveResult is one decision's outcome.
type ResolveResult struct {
	ID     string `json:"id"`
	OK     bool   `json:"ok"`
	Status string `json:"status,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Resolved is a decision call's answer.
type Resolved struct {
	Results         []ResolveResult `json:"results"`
	EditSessionID   string          `json:"edit_session_id,omitempty"`
	VersionNumber   *int            `json:"version_number,omitempty"`
	AwaitingPublish bool            `json:"awaiting_publish,omitempty"`
}

// WorkText is a work's text as an anchor needs it.
type WorkText struct {
	Kind            string `json:"kind"`
	Language        string `json:"language"`
	Content         string `json:"content"`
	CurrentVersion  *int   `json:"current_version"`
	ContentLanguage *struct {
		Served string `json:"served"`
	} `json:"content_language"`
}

func annotationsPath(workID string) string {
	return "/works/" + url.PathEscape(workID) + "/annotations"
}

// GetWorkText reads a single document's served text (`lang` empty: the original).
func (c *Client) GetWorkText(ctx context.Context, workID, lang string) (*WorkText, error) {
	var q url.Values
	if lang != "" {
		q = url.Values{"lang": {lang}}
	}
	var out WorkText
	if err := c.Get(ctx, workPath(workID), q, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetDivisionText reads one division's markdown off the reading page it is on.
func (c *Client) GetDivisionText(ctx context.Context, workID, divisionID string, page int) (string, error) {
	var out struct {
		Units []struct {
			ID     string `json:"id"`
			BodyMD string `json:"body_md"`
		} `json:"units"`
	}
	path := fmt.Sprintf("%s/read/%d", workPath(workID), page)
	if err := c.Get(ctx, path, nil, &out); err != nil {
		return "", err
	}
	for _, u := range out.Units {
		if strings.EqualFold(u.ID, divisionID) {
			return u.BodyMD, nil
		}
	}
	return "", &Error{Code: "not_found", Message: fmt.Sprintf(
		"division %s is not on page %d; `lt work show` lists the book's pages", divisionID, page)}
}

// ListAnnotations reads a work's annotations as the caller may read them.
func (c *Client) ListAnnotations(ctx context.Context, workID string, kinds, statuses []string, lang string) ([]Annotation, int, error) {
	q := url.Values{"limit": {"100"}}
	for _, k := range kinds {
		q.Add("kind", k)
	}
	for _, s := range statuses {
		q.Add("status", s)
	}
	if lang != "" {
		q.Set("lang", lang)
	}
	var out struct {
		Items []Annotation `json:"items"`
		Total int          `json:"total"`
	}
	if err := c.Get(ctx, annotationsPath(workID), q, &out); err != nil {
		return nil, 0, err
	}
	return out.Items, out.Total, nil
}

// CreateAnnotation writes a note, comment, suggestion or erratum.
func (c *Client) CreateAnnotation(ctx context.Context, workID string, in AnnotationCreate) (*Annotation, error) {
	var out Annotation
	if err := c.Post(ctx, annotationsPath(workID), in, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ResolveAnnotations decides suggested edits and errata on a work the caller
// may edit; `client` names lt on an accept's edit session.
func (c *Client) ResolveAnnotations(ctx context.Context, workID string, ids []string, action, summary, note string) (*Resolved, error) {
	body := map[string]any{"ids": ids, "action": action, "client": "lt"}
	if summary != "" {
		body["summary"] = summary
	}
	if note != "" {
		body["note"] = note
	}
	var out Resolved
	if err := c.Post(ctx, annotationsPath(workID)+"/resolve", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// quoteContext is how much text either side of a quote the anchor keeps.
const quoteContext = 32

// utf16Len counts s in UTF-16 code units.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// lastRunes and firstRunes cut context on character boundaries.
func lastRunes(s string, n int) string {
	for i := len(s); i > 0; {
		_, size := utf8.DecodeLastRuneInString(s[:i])
		i -= size
		if n--; n == 0 {
			return s[i:]
		}
	}
	return s
}

func firstRunes(s string, n int) string {
	for i := 0; i < len(s); {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
		if n--; n == 0 {
			return s[:i]
		}
	}
	return s
}

// AnchorFor builds the anchor of `quote`, which must occur exactly once in
// `source` -- a quote that is missing or repeats is refused, with what to do.
func AnchorFor(source, quote string, target AnchorTarget, version *int) (*Anchor, error) {
	if quote == "" {
		return nil, &Error{Code: "validation_error", Message: "quote the passage: its exact markdown"}
	}
	at := strings.Index(source, quote)
	if at < 0 {
		return nil, &Error{Code: "not_found", Message: "that passage does not occur in the text; quote the " +
			"markdown exactly (formulas as $...$, emphasis as **...**)"}
	}
	if n := strings.Count(source, quote); n > 1 {
		return nil, &Error{Code: "validation_error", Message: fmt.Sprintf(
			"that passage occurs %d times; quote a little more around it so it is unique", n)}
	}
	end := at + len(quote)
	start16 := utf16Len(source[:at])
	return &Anchor{
		Target:  target,
		Version: version,
		Quote: AnchorQuote{
			Exact:  quote,
			Prefix: lastRunes(source[:at], quoteContext),
			Suffix: firstRunes(source[end:], quoteContext),
		},
		Hint: &AnchorHint{Start: start16, End: start16 + utf16Len(quote)},
	}, nil
}
