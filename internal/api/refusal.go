package api

import (
	"encoding/json"
	"fmt"
	"strings"
)

// The publish refusals: a 409 whose detail carries an `error_code` saying
// which rule of the type registry the request broke. Each gets its own stable
// code, because the advice differs and an agent should branch on the rule
// rather than on prose.
const (
	// CodeVenueRequired: a book or a scientific work stays a draft until a
	// venue publishes it. Retrying does not help; saving it as a draft does.
	CodeVenueRequired = "venue_required"
	// CodeWorkIncomplete: the work lacks a field its type requires. Retry
	// after adding the fields in Missing.
	CodeWorkIncomplete = "work_incomplete"
	// CodeFamilyChange: a published work cannot change between a page kind
	// and a scientific kind. Retrying does not help.
	CodeFamilyChange = "family_change"
)

// The server's error_code values, as works-service raises them.
const (
	refusalVenueRequired  = "VENUE_REQUIRED"
	refusalWorkIncomplete = "WORK_INCOMPLETE"
	refusalFamilyChange   = "FAMILY_CHANGE"
)

// Refusal is a publish the type registry refused. It wraps the *Error every
// caller already handles, so code that only wants Code and Message keeps
// working unchanged; a caller that wants the detail -- which fields are
// missing -- reaches for this type with errors.As.
type Refusal struct {
	Err *Error
	// Kind is the work's kind as the server named it. RequestedKind is the
	// kind a FAMILY_CHANGE asked for.
	Kind          string
	RequestedKind string
	// Missing are the registry's field keys a WORK_INCOMPLETE named
	// (`title`, `language`, `body`, ...), in the server's order.
	Missing []string
}

func (r *Refusal) Error() string { return r.Err.Message }
func (r *Refusal) Unwrap() error { return r.Err }

// VenueRequired is the refusal lt raises itself, before any write, when the
// registry already says how the server would answer. Same code and words as
// the server's, so a dry run and a real run say the same thing.
func VenueRequired(kind string) *Refusal {
	return &Refusal{
		Err:  &Error{Code: CodeVenueRequired, Message: venueRequiredText(kind)},
		Kind: kind,
	}
}

func venueRequiredText(kind string) string {
	which := "so your own publish cannot take this one out of draft"
	if kind != "" {
		which = fmt.Sprintf("and this one's type is `%s`, so your own publish cannot "+
			"take it out of draft", kind)
	}
	return "Books and scientific works stay drafts until a venue publishes them, " +
		which + ". Nothing was published. To save it as a draft, set " +
		"`is_draft: true` in its frontmatter and re-run."
}

func workIncompleteText(missing []string) string {
	if len(missing) == 0 {
		return "This work is missing a field its type requires before it can be " +
			"published, and the API did not say which. Nothing was published. " +
			"`lt types` lists what each type requires."
	}
	words := make([]string, 0, len(missing))
	for _, key := range missing {
		words = append(words, MissingFieldText(key))
	}
	them := "them"
	if len(missing) == 1 {
		them = "it"
	}
	return "This work is missing what its type needs before it can be published: " +
		joinWords(words) + ". Nothing was published. Add " + them + " and re-run, " +
		"or set `is_draft: true` to keep it as a draft until then."
}

func familyChangeText(kind, requested string) string {
	msg := "A published work cannot move between pages and scientific works"
	if kind != "" && requested != "" {
		msg += fmt.Sprintf(": this one is `%s`, and the run asks for `%s`", kind, requested)
	}
	return msg + ". Nothing was changed. Keep its `type`, or publish the new text " +
		"as a separate work under another `import_key`."
}

// missingFields says each registry field key in words, and where lt takes it
// from. Presentation only: which keys a type requires is the registry's, never
// this table's. A key the table does not know is still shown, by its name.
var missingFields = map[string]string{
	"title":             "a title (`title`)",
	"language":          "the language it is written in (`language`)",
	"body":              "its text (the file's body, below the frontmatter; a book's chapters)",
	"description":       "a description (`description`)",
	"abstract":          "an abstract (`description`)",
	"license":           "a license (`license`)",
	"byline":            "an author on the byline (`contributors`, role `author`)",
	"advisor":           "an advisor on the byline (`contributors`, role `advisor`)",
	"thesis_level":      "the thesis level (set it on the site)",
	"category_ids":      "a category (`category_ids`)",
	"keywords":          "keywords (set them on the site)",
	"genres":            "a genre (set it on the site)",
	"references":        "its references (set them on the site)",
	"affiliated_org_id": "an affiliated organization (set it on the site)",
	"cover_image_id":    "a cover image (set it on the site)",
	"first_published":   "when it was first published (set it on the site)",
	"type":              "a type",
	"year":              "a year",
	"container_title":   "the journal or venue it appeared in",
	"doi_or_url":        "a DOI, arXiv id, PubMed id or URL",
}

// MissingFieldText is one registry field key in words.
func MissingFieldText(key string) string {
	if text, ok := missingFields[key]; ok {
		return text
	}
	return "`" + key + "`"
}

func joinWords(words []string) string {
	switch len(words) {
	case 0:
		return ""
	case 1:
		return words[0]
	default:
		return strings.Join(words[:len(words)-1], ", ") + " and " + words[len(words)-1]
	}
}

// responseError is FromResponse, plus the publish refusals. Every HTTP failure
// in this package goes through it.
//
// It never inspects a 404 (FromResponse's rule): the refusals are all 409s.
func responseError(status int, body []byte, context string) error {
	if status == 409 {
		if r := refusalOf(body); r != nil {
			return r
		}
	}
	return FromResponse(status, body, context)
}

// refusalOf reads a registry refusal out of a 409 body, or returns nil.
//
// The detail is an object on both envelopes the BFF relays:
//
//	{"error": {"status_code": 409, "detail": {"error_code": ..., ...}}}
//	{"detail": {"error_code": ..., ...}}
//
// The words are lt's, not the server's, because lt knows what to do next in
// its own terms (the frontmatter key, the re-run); the server's message is for
// the web editor.
func refusalOf(body []byte) *Refusal {
	detail := refusalDetail(body)
	if detail == nil {
		return nil
	}
	code, _ := detail["error_code"].(string)
	kind, _ := detail["kind"].(string)
	switch code {
	case refusalVenueRequired:
		return VenueRequired(kind)
	case refusalWorkIncomplete:
		missing := stringsOf(detail["missing"])
		return &Refusal{
			Err:     &Error{Code: CodeWorkIncomplete, Message: workIncompleteText(missing)},
			Missing: missing,
		}
	case refusalFamilyChange:
		requested, _ := detail["requested_kind"].(string)
		return &Refusal{
			Err:           &Error{Code: CodeFamilyChange, Message: familyChangeText(kind, requested)},
			Kind:          kind,
			RequestedKind: requested,
		}
	}
	return nil
}

func refusalDetail(body []byte) map[string]any {
	var obj map[string]any
	if len(body) == 0 || json.Unmarshal(body, &obj) != nil {
		return nil
	}
	if e, ok := obj["error"].(map[string]any); ok {
		if d, ok := e["detail"].(map[string]any); ok {
			return d
		}
	}
	if d, ok := obj["detail"].(map[string]any); ok {
		return d
	}
	return nil
}

func stringsOf(v any) []string {
	items, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s, ok := item.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out
}
