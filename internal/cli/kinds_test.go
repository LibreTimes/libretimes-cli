package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/The-LibreTimes/libretimes-cli/internal/api"
)

// A registry shaped like the one the BFF serves: one kind an author
// publishes, and two a venue does.
const testRegistry = `{"version":1,"kinds":[` +
	`{"kind":"lecture","family":"page","strictness":"standard","publish":"self","direction":"education",` +
	`"csl_type":"speech","lead":{"label":"description","on_page":true,"max":500},` +
	`"required":["title","language","body"],"recommended":["description"],"significant":["title"],"scholarly":false},` +
	`{"kind":"original_research","family":"scientific","strictness":"record","publish":"venue","direction":"science",` +
	`"csl_type":"article-journal","lead":{"label":"abstract","on_page":true,"max":3000},` +
	`"required":["title","language","body","abstract","byline"],"recommended":["keywords"],"significant":["title"],"scholarly":true},` +
	`{"kind":"book","family":"scientific","strictness":"record","publish":"venue","direction":"other",` +
	`"csl_type":"book","lead":{"label":"description","on_page":true,"max":2000},` +
	`"required":["title","language","body"],"recommended":[],"significant":[],"scholarly":false}],` +
	`"external_types":[],"external_fallback":"other"}`

func serveRegistry(next func(w http.ResponseWriter, r *http.Request) bool) func(w http.ResponseWriter, r *http.Request) bool {
	return func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/work-types" {
			w.Write([]byte(testRegistry))
			return true
		}
		return next != nil && next(w, r)
	}
}

func notImported(w http.ResponseWriter, r *http.Request) bool {
	if strings.HasSuffix(r.URL.Path, "/by-import-key") {
		w.WriteHeader(http.StatusNotFound)
		return true
	}
	return false
}

func docOfType(kind, extra string) string {
	return "---\ntitle: On a result\ntype: " + kind + "\nimport_key: k/1\nlanguage: en\n" + extra + "---\nBody."
}

// lt keeps no list of kinds: a `type` is checked against the server's
// registry, before anything is written, and the refusal names the real ones.
func TestUnknownTypeIsRefusedFromTheRegistry(t *testing.T) {
	rec := newRecorder(t, serveRegistry(notImported))
	path := writeFile(t, t.TempDir(), "doc.md", docOfType("lectrue", ""))

	result := publishOne(context.Background(), api.New(rec.server.URL, "tok", "test"), path, false, nil)

	if result.Action != actionFailed || result.Code != api.CodeValidation {
		t.Fatalf("action/code = %s/%s, want failed/validation_error (%s)", result.Action, result.Code, result.Message)
	}
	for _, want := range []string{`"lectrue"`, "lecture, original_research, book"} {
		if !strings.Contains(result.Message, want) {
			t.Errorf("message lacks %q: %s", want, result.Message)
		}
	}
	if rec.writeSum != 0 {
		t.Errorf("an unknown type still wrote: %s", rec.calls())
	}
}

// A scientific work the file would publish is refused before any write, on a
// dry run and on a real one alike -- the plan never promises what the server
// would refuse.
func TestVenueKindIsRefusedBeforeAnyWrite(t *testing.T) {
	for _, dryRun := range []bool{true, false} {
		for _, extra := range []string{"", "is_draft: false\n"} {
			rec := newRecorder(t, serveRegistry(notImported))
			path := writeFile(t, t.TempDir(), "doc.md", docOfType("original_research", extra))

			result := publishOne(context.Background(), api.New(rec.server.URL, "tok", "test"), path, dryRun, nil)

			if result.Action != actionFailed || result.Code != api.CodeVenueRequired {
				t.Fatalf("dry=%v %q: action/code = %s/%s, want failed/venue_required",
					dryRun, extra, result.Action, result.Code)
			}
			if !strings.Contains(result.Message, "`is_draft: true`") {
				t.Errorf("the refusal does not say how to save it as a draft: %s", result.Message)
			}
			if rec.writeSum != 0 {
				t.Errorf("dry=%v: refused, but still wrote: %s", dryRun, rec.calls())
			}
		}
	}
}

func TestVenueKindAsADraftIsCreated(t *testing.T) {
	rec := newRecorder(t, serveRegistry(notImported))
	path := writeFile(t, t.TempDir(), "doc.md", docOfType("original_research", "is_draft: true\n"))

	result := publishOne(context.Background(), api.New(rec.server.URL, "tok", "test"), path, false, nil)

	if result.Action != actionCreate {
		t.Fatalf("action = %s (%s: %s)", result.Action, result.Code, result.Message)
	}
	if got := rec.bodies[0]["is_draft"]; got != true {
		t.Errorf("created with is_draft = %v", got)
	}
}

// Updating a venue kind that is already published publishes nothing, so it is
// not the gate's business.
func TestVenueKindAlreadyPublishedUpdates(t *testing.T) {
	rec := newRecorder(t, serveRegistry(func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/by-import-key") {
			w.Write([]byte(`{"id":"pub-3","import_key":"k/1","is_draft":false,"removed":false}`))
			return true
		}
		return false
	}))
	path := writeFile(t, t.TempDir(), "doc.md", docOfType("original_research", ""))

	result := publishOne(context.Background(), api.New(rec.server.URL, "tok", "test"), path, false, nil)

	if result.Action != actionUpdate {
		t.Fatalf("action = %s (%s: %s)", result.Action, result.Code, result.Message)
	}
}

// When the registry cannot be read, the server's own 409 is the refusal, and
// lt explains it without retrying: one create, and a failed result.
func TestServerVenueRefusalIsExplainedAndNotRetried(t *testing.T) {
	rec := newRecorder(t, func(w http.ResponseWriter, r *http.Request) bool {
		switch {
		case r.URL.Path == "/work-types":
			w.WriteHeader(http.StatusNotFound)
		case strings.HasSuffix(r.URL.Path, "/by-import-key"):
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusConflict)
			w.Write([]byte(`{"error":{"status_code":409,"type":"conflict","detail":{` +
				`"error_code":"VENUE_REQUIRED","message":"...","kind":"original_research"}}}`))
		default:
			return false
		}
		return true
	})
	path := writeFile(t, t.TempDir(), "doc.md", docOfType("original_research", ""))

	result := publishOne(context.Background(), api.New(rec.server.URL, "tok", "test"), path, false, nil)

	if result.Code != api.CodeVenueRequired {
		t.Fatalf("code = %q (%s)", result.Code, result.Message)
	}
	if rec.writeSum != 1 {
		t.Errorf("%d writes, want exactly the one refused create: %s", rec.writeSum, rec.calls())
	}
}

func TestServerIncompleteRefusalCarriesTheMissingKeys(t *testing.T) {
	rec := newRecorder(t, func(w http.ResponseWriter, r *http.Request) bool {
		switch {
		case strings.HasSuffix(r.URL.Path, "/by-import-key"):
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPost:
			w.WriteHeader(http.StatusConflict)
			w.Write([]byte(`{"error":{"detail":{"error_code":"WORK_INCOMPLETE","missing":["license","category_ids"]}}}`))
		default:
			return false
		}
		return true
	})
	path := writeFile(t, t.TempDir(), "doc.md", docOfType("lecture", ""))

	result := publishOne(context.Background(), api.New(rec.server.URL, "tok", "test"), path, false, nil)

	if result.Code != api.CodeWorkIncomplete {
		t.Fatalf("code = %q (%s)", result.Code, result.Message)
	}
	if !slices.Equal(result.Missing, []string{"license", "category_ids"}) {
		t.Errorf("missing = %v", result.Missing)
	}
	if !strings.Contains(result.Message, "a license (`license`) and a category (`category_ids`)") {
		t.Errorf("message does not list the fields in words: %s", result.Message)
	}

	encoded, _ := json.Marshal(result)
	if !strings.Contains(string(encoded), `"missing":["license","category_ids"]`) {
		t.Errorf("--json result lacks missing: %s", encoded)
	}
}

func TestServerFamilyChangeIsExplained(t *testing.T) {
	rec := newRecorder(t, func(w http.ResponseWriter, r *http.Request) bool {
		switch {
		case strings.HasSuffix(r.URL.Path, "/by-import-key"):
			w.Write([]byte(`{"id":"pub-4","import_key":"k/1","is_draft":false,"removed":false}`))
		case r.Method == http.MethodPatch:
			w.WriteHeader(http.StatusConflict)
			w.Write([]byte(`{"error":{"detail":{"error_code":"FAMILY_CHANGE","kind":"lecture","requested_kind":"report"}}}`))
		default:
			return false
		}
		return true
	})
	path := writeFile(t, t.TempDir(), "doc.md", docOfType("report", ""))

	result := publishOne(context.Background(), api.New(rec.server.URL, "tok", "test"), path, false, nil)

	if result.Code != api.CodeFamilyChange {
		t.Fatalf("code = %q (%s)", result.Code, result.Message)
	}
	if !strings.Contains(result.Message, "between pages and scientific works") {
		t.Errorf("message does not explain: %s", result.Message)
	}
}

// A book is created as a draft and published once its chapters are written;
// when a venue publishes books, the run stops before the first write instead
// of after the last chapter.
func TestBookVenueRefusalComesBeforeTheChapters(t *testing.T) {
	f := newBookAPI(t)
	f.registry = testRegistry
	dir := t.TempDir()
	writeFile(t, dir, "one.md", "Chapter one.")
	manifest := writeFile(t, dir, "book.md", "---\nimport_key: a/b\ntitle: A Book\nlanguage: en\n"+
		"contents:\n  - file: one.md\n    kind: chapter\n    label: \"1\"\n---\n")

	client := api.New(f.serve().URL, "tok", "test")
	result := publishBook(context.Background(), client, manifest, bookOptions{profiles: newProfileResolver(client)})

	if result.Code != api.CodeVenueRequired {
		t.Fatalf("code = %q (%s)", result.Code, result.Message)
	}
	if f.createdPub != nil || f.divisionWrite != 0 {
		t.Errorf("wrote before refusing: %v", f.calls)
	}
}

func TestTypesCommandListsTheRegistry(t *testing.T) {
	rec := newRecorder(t, serveRegistry(nil))

	var out bytes.Buffer
	root, e := NewRootCommand()
	e.out, e.err = &out, &out
	root.SetArgs([]string{"types", "--api-url", rec.server.URL})
	t.Setenv("LIBRETIMES_TOKEN", "")
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"KIND", "lecture", "you", "original_research", "a venue",
		"title, language, body, abstract, byline"} {
		if !strings.Contains(text, want) {
			t.Errorf("output lacks %q:\n%s", want, text)
		}
	}

	out.Reset()
	root, e = NewRootCommand()
	e.out, e.err = &out, &out
	root.SetArgs([]string{"types", "book", "--json", "--api-url", rec.server.URL})
	if err := root.ExecuteContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	var kind api.KindType
	if err := json.Unmarshal(out.Bytes(), &kind); err != nil || kind.Kind != "book" || kind.Publish != "venue" {
		t.Errorf("--json for one kind = %s (%v)", out.String(), err)
	}

	root, e = NewRootCommand()
	e.out, e.err = &out, &out
	root.SetArgs([]string{"types", "essay", "--api-url", rec.server.URL})
	err := root.ExecuteContext(context.Background())
	if _, _, exit := classify(err); exit != ExitBadArguments {
		t.Errorf("an unknown kind exits %d, want %d (%v)", exit, ExitBadArguments, err)
	}
}
