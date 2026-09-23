package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/The-LibreTimes/libretimes-cli/internal/api"
)

// fakeDivision is one node of the fake's tree. Children are ordered by the
// slice they sit in, which is all the ordinal is for.
type fakeDivision struct {
	ID        string  `json:"id"`
	ParentID  *string `json:"parent_id"`
	Kind      string  `json:"kind"`
	Label     string  `json:"label"`
	Title     *string `json:"title"`
	FirstLine *string `json:"first_line"`
	Ref       *string `json:"ref"`
	BodyMD    *string `json:"body_md"`
}

// bookAPI is a stateful fake of the slice of the API `lt book publish` touches,
// so a second run can be checked against what the first one left behind.
// Reading numbers are the draft rule: body-bearing nodes numbered in reading
// order, recomputed on every read.
type bookAPI struct {
	t        *testing.T
	pubType  string // "" = no publication holds the key yet
	removed  bool
	isDraft  bool
	language string

	nodes    map[string]*fakeDivision
	children map[string][]string // parent id ("" = root) -> ids in order
	nextID   int

	// rewrite, when set, is what the server does to a stored body.
	rewrite func(string) string

	calls         []string
	createdPub    map[string]any
	pubPatches    []map[string]any
	divisionWrite int
	deleted       []string
}

func newBookAPI(t *testing.T) *bookAPI {
	return &bookAPI{t: t, nodes: map[string]*fakeDivision{}, children: map[string][]string{}}
}

func (f *bookAPI) serve() *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(f.handle))
	f.t.Cleanup(srv.Close)
	return srv
}

const fakeWork = "work-1"
const fakeExpr = "expr-1"

func (f *bookAPI) handle(w http.ResponseWriter, r *http.Request) {
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	var body map[string]any
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	divs := "/works/" + fakeWork + "/expressions/" + fakeExpr + "/divisions"
	path := r.URL.Path
	switch {
	case r.Method == http.MethodGet && path == "/publications/me/by-import-key":
		if f.pubType == "" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"id": fakeWork, "import_key": r.URL.Query().Get("key"),
			"is_draft": f.isDraft, "removed": f.removed,
		})
	case r.Method == http.MethodPost && path == "/publications":
		f.createdPub = body
		f.pubType, f.isDraft = body["type"].(string), body["is_draft"] == true
		f.language, _ = body["language"].(string)
		json.NewEncoder(w).Encode(map[string]any{"id": fakeWork, "is_draft": f.isDraft})
	case r.Method == http.MethodGet && path == "/publications/"+fakeWork:
		primary := map[string]string{}
		if f.pubType == api.TypeBook {
			primary[f.language] = fakeExpr
		}
		json.NewEncoder(w).Encode(map[string]any{
			"id": fakeWork, "type": f.pubType, "language": f.language, "is_draft": f.isDraft,
			"primary_expression_by_language": primary,
		})
	case r.Method == http.MethodPatch && path == "/publications/"+fakeWork:
		f.pubPatches = append(f.pubPatches, body)
		if d, ok := body["is_draft"].(bool); ok {
			if !d && f.readingUnits() == 0 {
				w.WriteHeader(http.StatusConflict)
				w.Write([]byte(`{"detail":"hosted but have no readable text"}`))
				return
			}
			f.isDraft = d
		}
		json.NewEncoder(w).Encode(map[string]any{"id": fakeWork, "is_draft": f.isDraft})
	case r.Method == http.MethodGet && path == "/works/"+fakeWork+"/contents":
		if r.URL.Query().Get("e") != fakeExpr {
			f.t.Errorf("contents read without ?e=%s: %s", fakeExpr, r.URL.RawQuery)
		}
		json.NewEncoder(w).Encode(map[string]any{"expression_id": fakeExpr, "tree": f.tree("")})
	case r.Method == http.MethodPost && path == divs:
		f.divisionWrite++
		f.nextID++
		id := fmt.Sprintf("d%d", f.nextID)
		node := &fakeDivision{ID: id, Kind: body["kind"].(string), Label: str(body["label"]),
			Title: ptr(body["title"]), FirstLine: ptr(body["first_line"]), Ref: ptr(body["ref"])}
		parent := ""
		if p, ok := body["parent_id"].(string); ok {
			parent = p
			node.ParentID = &p
		}
		f.nodes[id] = node
		f.place(id, parent, int(body["index"].(float64)))
		json.NewEncoder(w).Encode(node)
	case strings.HasPrefix(path, divs+"/"):
		rest := strings.TrimPrefix(path, divs+"/")
		id, move := strings.CutSuffix(rest, "/move")
		node := f.nodes[id]
		if node == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		switch {
		case r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(node)
		case r.Method == http.MethodPost && move:
			f.divisionWrite++
			parent := ""
			node.ParentID = nil
			if p, ok := body["parent_id"].(string); ok {
				parent = p
				node.ParentID = &p
			}
			f.unlink(id)
			f.place(id, parent, int(body["index"].(float64)))
			json.NewEncoder(w).Encode(node)
		case r.Method == http.MethodPatch:
			f.divisionWrite++
			for k, v := range body {
				switch k {
				case "kind":
					node.Kind = v.(string)
				case "label":
					node.Label = v.(string)
				case "title":
					node.Title = ptr(v)
				case "first_line":
					node.FirstLine = ptr(v)
				case "ref":
					node.Ref = ptr(v)
				case "body_md":
					node.BodyMD = ptr(v)
					if node.BodyMD != nil && f.rewrite != nil {
						s := f.rewrite(*node.BodyMD)
						node.BodyMD = &s
					}
				}
			}
			json.NewEncoder(w).Encode(node)
		case r.Method == http.MethodDelete:
			f.divisionWrite++
			f.deleted = append(f.deleted, id)
			f.drop(id)
			w.WriteHeader(http.StatusNoContent)
		}
	case r.Method == http.MethodGet && strings.HasSuffix(path, "/authors"):
		w.Write([]byte(`[]`))
	default:
		f.t.Errorf("unexpected call %s %s", r.Method, path)
		w.WriteHeader(http.StatusTeapot)
	}
}

func (f *bookAPI) place(id, parent string, index int) {
	list := f.children[parent]
	index = min(index, len(list))
	f.children[parent] = slices.Insert(list, index, id)
}

func (f *bookAPI) unlink(id string) {
	for parent, list := range f.children {
		f.children[parent] = slices.DeleteFunc(list, func(c string) bool { return c == id })
	}
}

func (f *bookAPI) drop(id string) {
	for _, child := range slices.Clone(f.children[id]) {
		f.drop(child)
	}
	f.unlink(id)
	delete(f.children, id)
	delete(f.nodes, id)
}

func (f *bookAPI) readingOrder(parent string, out []string) []string {
	for _, id := range f.children[parent] {
		out = append(out, id)
		out = f.readingOrder(id, out)
	}
	return out
}

func (f *bookAPI) readingUnits() int {
	n := 0
	for _, id := range f.readingOrder("", nil) {
		if f.nodes[id].BodyMD != nil {
			n++
		}
	}
	return n
}

func (f *bookAPI) tree(parent string) []map[string]any {
	numbers := map[string]int{}
	n := 0
	for _, id := range f.readingOrder("", nil) {
		if f.nodes[id].BodyMD != nil {
			n++
			numbers[id] = n
		}
	}
	var build func(parent string) []map[string]any
	build = func(parent string) []map[string]any {
		out := []map[string]any{}
		for _, id := range f.children[parent] {
			node := f.nodes[id]
			entry := map[string]any{"id": id, "kind": node.Kind, "label": node.Label,
				"title": node.Title, "first_line": node.FirstLine, "ref": node.Ref,
				"children": build(id)}
			if num, ok := numbers[id]; ok {
				entry["reading_number"] = num
			}
			out = append(out, entry)
		}
		return out
	}
	return build(parent)
}

// outline renders the fake's tree as "label(kind)" lines, indented by depth.
func (f *bookAPI) outline() string {
	var b strings.Builder
	var walk func(parent string, depth int)
	walk = func(parent string, depth int) {
		for _, id := range f.children[parent] {
			node := f.nodes[id]
			fmt.Fprintf(&b, "%s%s(%s)", strings.Repeat("  ", depth), node.Label, node.Kind)
			if node.BodyMD != nil {
				fmt.Fprintf(&b, ":%s", strings.TrimSpace(*node.BodyMD))
			}
			b.WriteString("\n")
			walk(id, depth+1)
		}
	}
	walk("", 0)
	return b.String()
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func ptr(v any) *string {
	s, ok := v.(string)
	if !ok {
		return nil
	}
	return &s
}

const bookManifest = `---
import_key: ivanov/analysis-book
title: Analysis
language: en
contents:
  - file: preface.md
    kind: front_matter
    label: Preface
  - label: Part I
    title: Limits
    children:
      - file: 01.md
        label: "1"
        ref: I.1
      - file: 02.md
        label: "2"
        ref: I.2
---
Working notes.`

func writeBook(t *testing.T, manifest string, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	defaults := map[string]string{
		"preface.md": "Why this book.",
		"01.md":      "---\ntitle: Sequences\n---\nA sequence is a function.",
		"02.md":      "Series are sums.",
	}
	for name, content := range defaults {
		if _, override := files[name]; !override {
			writeFile(t, dir, name, content)
		}
	}
	for name, content := range files {
		writeFile(t, dir, name, content)
	}
	return writeFile(t, dir, "book.md", manifest)
}

func runBook(t *testing.T, f *bookAPI, path string, opts bookOptions) bookResult {
	t.Helper()
	srv := f.serve()
	return publishBook(context.Background(), api.New(srv.URL, "tok", "test"), path, opts)
}

func chapterActions(r bookResult) []string {
	var out []string
	for _, c := range r.Chapters {
		out = append(out, c.Action+strings.Join(append([]string{""}, c.Changes...), ":"))
	}
	return out
}

const wantOutline = `Preface(front_matter):Why this book.
Part I(part)
  1(chapter):A sequence is a function.
  2(chapter):Series are sums.
`

func TestBookCreateWritesTheTreeThenPublishes(t *testing.T) {
	f := newBookAPI(t)
	result := runBook(t, f, writeBook(t, bookManifest, nil), bookOptions{})

	if result.Action != actionCreate || result.Code != "" {
		t.Fatalf("result = %+v", result)
	}
	if f.createdPub["type"] != "book" || f.createdPub["is_draft"] != true ||
		f.createdPub["import_key"] != "ivanov/analysis-book" {
		t.Errorf("create body = %v", f.createdPub)
	}
	if _, ok := f.createdPub["content"]; ok {
		t.Errorf("a book is created with no content: %v", f.createdPub)
	}
	if got := f.outline(); got != wantOutline {
		t.Errorf("tree =\n%s\nwant\n%s", got, wantOutline)
	}
	// The part's kind defaults from its shape; the file's title fills in.
	if result.Chapters[1].Kind != "part" || result.Chapters[2].Title != "Sequences" {
		t.Errorf("chapters = %+v", result.Chapters)
	}
	last := f.pubPatches[len(f.pubPatches)-1]
	if last["is_draft"] != false || !result.Published || f.isDraft {
		t.Errorf("the book should be published last: patches=%v published=%v", f.pubPatches, result.Published)
	}
	var numbers []int
	for _, c := range result.Chapters {
		if c.ReadingNumber != nil {
			numbers = append(numbers, *c.ReadingNumber)
		}
	}
	if !slices.Equal(numbers, []int{1, 2, 3}) {
		t.Errorf("reading numbers = %v, want 1 2 3 read back after the write", numbers)
	}
}

func TestBookReRunChangesNothing(t *testing.T) {
	f := newBookAPI(t)
	path := writeBook(t, bookManifest, nil)
	runBook(t, f, path, bookOptions{})
	writes := f.divisionWrite

	result := runBook(t, f, path, bookOptions{})
	if result.Action != actionUpdate {
		t.Fatalf("result = %+v", result)
	}
	if f.divisionWrite != writes {
		t.Errorf("a re-run wrote %d division(s): %v", f.divisionWrite-writes, f.calls)
	}
	for _, a := range chapterActions(result) {
		if a != chapterUnchanged {
			t.Errorf("actions = %v, want all unchanged", chapterActions(result))
			break
		}
	}
	if result.Published {
		t.Error("an already published book is not published again")
	}
}

// Chapters with a ref keep their identity across a reorder: one move, no body
// rewritten, and the text stays with its chapter.
func TestBookReorderByRefMovesInsteadOfRewriting(t *testing.T) {
	f := newBookAPI(t)
	runBook(t, f, writeBook(t, bookManifest, nil), bookOptions{})
	before := f.divisionWrite

	swapped := strings.Replace(bookManifest, `      - file: 01.md
        label: "1"
        ref: I.1
      - file: 02.md
        label: "2"
        ref: I.2`, `      - file: 02.md
        label: "2"
        ref: I.2
      - file: 01.md
        label: "1"
        ref: I.1`, 1)
	result := runBook(t, f, writeBook(t, swapped, nil), bookOptions{})

	if f.divisionWrite-before != 1 {
		t.Errorf("wrote %d times, want one move: %v", f.divisionWrite-before, f.calls)
	}
	want := "Preface(front_matter):Why this book.\nPart I(part)\n  2(chapter):Series are sums.\n  1(chapter):A sequence is a function.\n"
	if got := f.outline(); got != want {
		t.Errorf("tree =\n%s", got)
	}
	if acts := chapterActions(result); !slices.Equal(acts, []string{"unchanged", "unchanged", "update:position", "unchanged"}) {
		t.Errorf("actions = %v", acts)
	}
}

func TestBookDryRunReportsTheRealPlanAndWritesNothing(t *testing.T) {
	edited := strings.Replace(bookManifest, "label: Preface", "label: Foreword", 1)
	edited = strings.Replace(edited, `      - file: 01.md
        label: "1"
        ref: I.1
      - file: 02.md
        label: "2"
        ref: I.2`, `      - file: 02.md
        label: "2"
        ref: I.2
      - file: 03.md
        label: "3"
      - file: 01.md
        label: "1"
        ref: I.1`, 1)

	dry, real := newBookAPI(t), newBookAPI(t)
	runBook(t, dry, writeBook(t, bookManifest, nil), bookOptions{})
	runBook(t, real, writeBook(t, bookManifest, nil), bookOptions{})
	files := map[string]string{"03.md": "Continuity.", "02.md": "Series, rewritten."}

	before := dry.divisionWrite
	planned := runBook(t, dry, writeBook(t, edited, files), bookOptions{dryRun: true})
	if dry.divisionWrite != before || len(dry.pubPatches) != 1 {
		t.Errorf("--dry-run wrote: %v", dry.calls)
	}
	done := runBook(t, real, writeBook(t, edited, files), bookOptions{})

	if !slices.Equal(chapterActions(planned), chapterActions(done)) {
		t.Errorf("dry run planned %v, the real run did %v", chapterActions(planned), chapterActions(done))
	}
	want := []string{"update:label", "unchanged", "update:body:position", "create", "unchanged"}
	if !slices.Equal(chapterActions(done), want) {
		t.Errorf("actions = %v, want %v", chapterActions(done), want)
	}
}

func TestBookUnlistedChapterRefusesWithoutPrune(t *testing.T) {
	f := newBookAPI(t)
	runBook(t, f, writeBook(t, bookManifest, nil), bookOptions{})
	before := f.divisionWrite

	short := strings.Replace(bookManifest, `      - file: 02.md
        label: "2"
        ref: I.2
`, "", 1)
	result := runBook(t, f, writeBook(t, short, nil), bookOptions{})

	if result.Action != actionFailed || result.Code != "unlisted_chapters" {
		t.Fatalf("result = %+v", result)
	}
	if len(result.Unlisted) != 1 || result.Unlisted[0].Label != "2" || result.Unlisted[0].Action != chapterUnlisted {
		t.Errorf("unlisted = %+v", result.Unlisted)
	}
	if f.divisionWrite != before || len(f.pubPatches) != 1 {
		t.Errorf("a refused run wrote: %v", f.calls)
	}

	result = runBook(t, f, writeBook(t, short, nil), bookOptions{prune: true})
	if result.Action != actionUpdate || len(f.deleted) != 1 || result.Unlisted[0].Action != chapterDelete {
		t.Errorf("prune: result=%+v deleted=%v", result, f.deleted)
	}
}

// An unlisted part goes with its unlisted children in one deletion, after the
// chapter the manifest still names has moved out from under it.
func TestBookPruneDeletesSubtreeOnceAfterRescuingListedChildren(t *testing.T) {
	f := newBookAPI(t)
	runBook(t, f, writeBook(t, bookManifest, nil), bookOptions{})

	flat := `---
import_key: ivanov/analysis-book
title: Analysis
language: en
contents:
  - file: preface.md
    kind: front_matter
    label: Preface
  - file: 01.md
    label: "1"
    ref: I.1
---
`
	result := runBook(t, f, writeBook(t, flat, nil), bookOptions{prune: true})
	if result.Action != actionUpdate {
		t.Fatalf("result = %+v", result)
	}
	if len(f.deleted) != 1 {
		t.Errorf("deleted %v, want only the part (its remaining chapter goes with it)", f.deleted)
	}
	want := "Preface(front_matter):Why this book.\n1(chapter):A sequence is a function.\n"
	if got := f.outline(); got != want {
		t.Errorf("tree =\n%s", got)
	}
}

func TestBookStaysDraftWhenTheManifestSaysSo(t *testing.T) {
	f := newBookAPI(t)
	draft := strings.Replace(bookManifest, "language: en\n", "language: en\nis_draft: true\n", 1)
	result := runBook(t, f, writeBook(t, draft, nil), bookOptions{})
	if result.Published || !f.isDraft || len(f.pubPatches) != 0 {
		t.Errorf("published=%v draft=%v patches=%v", result.Published, f.isDraft, f.pubPatches)
	}
}

func TestBookRefusesAKeyHeldByAnotherType(t *testing.T) {
	f := newBookAPI(t)
	f.pubType, f.language = "lecture", "en"
	result := runBook(t, f, writeBook(t, bookManifest, nil), bookOptions{})
	if result.Code != "not_a_book" {
		t.Errorf("result = %+v", result)
	}
}

// A manifest in one language must never be written into a book's text in
// another: before translations it could only mean the book's one text, now it
// would overwrite the original with a translation.
func TestBookRefusesALanguageTheBookHasNoTextIn(t *testing.T) {
	f := newBookAPI(t)
	f.pubType, f.language = "book", "ru"
	result := runBook(t, f, writeBook(t, bookManifest, nil), bookOptions{})
	if result.Code != "no_expression" || result.Action != actionFailed {
		t.Fatalf("result = %+v", result)
	}
	if f.divisionWrite != 0 || len(f.pubPatches) != 0 {
		t.Errorf("wrote %d divisions and %d patches into the ru text", f.divisionWrite, len(f.pubPatches))
	}
}

func TestBookBlockedOnADeletedKey(t *testing.T) {
	f := newBookAPI(t)
	f.pubType, f.removed = "book", true
	result := runBook(t, f, writeBook(t, bookManifest, nil), bookOptions{})
	if result.Action != actionBlocked {
		t.Errorf("result = %+v", result)
	}
}

func TestBookWarnsWhenTheServerRewritesABody(t *testing.T) {
	f := newBookAPI(t)
	f.rewrite = func(s string) string { return strings.Replace(s, "# ", "## ", 1) }
	result := runBook(t, f, writeBook(t, bookManifest, map[string]string{"02.md": "# Series\nSums."}),
		bookOptions{})
	rewritten := slices.IndexFunc(result.Warnings, func(w string) bool {
		return strings.Contains(w, "02.md") && strings.Contains(w, "stored")
	})
	if rewritten < 0 {
		t.Errorf("warnings = %v, want one naming 02.md", result.Warnings)
	}
}

func TestBookWarnsOnPartialRefs(t *testing.T) {
	f := newBookAPI(t)
	result := runBook(t, f, writeBook(t, bookManifest, nil), bookOptions{dryRun: true})
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "2 of 3 chapters") {
		t.Errorf("warnings = %v", result.Warnings)
	}
}

func TestBookManifestValidation(t *testing.T) {
	cases := map[string]struct {
		manifest string
		files    map[string]string
		want     string
	}{
		"no key": {strings.Replace(bookManifest, "import_key: ivanov/analysis-book\n", "", 1), nil, "import_key"},
		"not a book": {strings.Replace(bookManifest, "language: en\n", "language: en\ntype: lecture\n", 1),
			nil, "`type` is \"lecture\""},
		"bad kind":   {strings.Replace(bookManifest, "kind: front_matter", "kind: appendix", 1), nil, "kind \"appendix\""},
		"dup ref":    {strings.Replace(bookManifest, "ref: I.2", "ref: I.1", 1), nil, "share the ref"},
		"conflict":   {bookManifest, map[string]string{"02.md": "---\nlabel: two\n---\nx"}, "in one place"},
		"no file":    {strings.Replace(bookManifest, "file: 02.md", "file: 09.md", 1), nil, "09.md"},
		"no content": {"---\nimport_key: k\ntitle: T\nlanguage: en\n---\n", nil, "`contents` is empty"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newBookAPI(t)
			result := runBook(t, f, writeBook(t, c.manifest, c.files), bookOptions{})
			if result.Action != actionFailed || !strings.Contains(result.Message, c.want) {
				t.Errorf("result = %+v, want a failure naming %q", result, c.want)
			}
			if len(f.calls) != 0 {
				t.Errorf("an invalid manifest reached the API: %v", f.calls)
			}
		})
	}
}
