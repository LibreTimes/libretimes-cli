package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/The-LibreTimes/libretimes-cli/internal/api"
)

// collectionAPI is a fake of the slice of the API `lt collection publish` touches.
// Publications are keyed by import_key; the collection exists when coll is set.
type collectionAPI struct {
	pubs  map[string]string // import_key -> JSON ref
	coll  string            // JSON ref for the course's import_key, "" = none
	items []string          // entity ids already in the collection, in order

	// oldServer answers a create without echoing import_key, as a server that
	// predates collection import keys does.
	oldServer  bool
	sectionSeq int
	createKey  string // the import_key a create echoes; default sh/an
	deleted    []string

	created   map[string]any
	patched   map[string]any
	added     []string
	reordered []string

	// Sections, rich items and the works API (the CL5 / CL4 surface).
	sections     []map[string]any          // current sections: id, title, position, description
	itemExtra    map[string]map[string]any // work id -> note / section_id / expression_id on the held item
	works        map[string]string         // GET /works/{id}
	resolve      map[string]string         // POST /works/resolve by query
	expressions  map[string]string         // GET /works/{id}/expressions
	orderBody    map[string]any            // the last PUT .../items/order body
	addedItems   []map[string]any          // full POST .../items entries
	itemPatches  map[string]map[string]any // work id -> PATCH body
	sectionNew   []map[string]any          // POST .../sections bodies
	sectionPatch map[string]map[string]any // section id -> PATCH body
}

func (f *collectionAPI) handler(t *testing.T) func(w http.ResponseWriter, r *http.Request) bool {
	return func(w http.ResponseWriter, r *http.Request) bool {
		path := r.URL.Path
		switch {
		case r.Method == http.MethodGet && path == "/works/me/by-import-key":
			ref, ok := f.pubs[r.URL.Query().Get("key")]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return true
			}
			w.Write([]byte(ref))
		case r.Method == http.MethodGet && path == "/collections/me/by-import-key":
			if f.coll == "" {
				w.WriteHeader(http.StatusNotFound)
				return true
			}
			w.Write([]byte(f.coll))
		case r.Method == http.MethodGet && strings.HasSuffix(path, "/items"):
			page := map[string]any{"total": len(f.items), "items": []map[string]any{}}
			for i, id := range f.items {
				row := map[string]any{"entity_type": "work", "entity_id": id, "position": i}
				for k, v := range f.itemExtra[id] {
					row[k] = v
				}
				page["items"] = append(page["items"].([]map[string]any), row)
			}
			json.NewEncoder(w).Encode(page)
		case r.Method == http.MethodGet && path == "/collections/coll-1":
			json.NewEncoder(w).Encode(map[string]any{"id": "coll-1", "sections": f.sections})
		case r.Method == http.MethodGet && strings.HasPrefix(path, "/works/") && strings.HasSuffix(path, "/expressions"):
			id := strings.TrimSuffix(strings.TrimPrefix(path, "/works/"), "/expressions")
			body, ok := f.expressions[id]
			if !ok {
				body = "[]"
			}
			w.Write([]byte(body))
		case r.Method == http.MethodGet && strings.HasPrefix(path, "/works/"):
			body, ok := f.works[strings.TrimPrefix(path, "/works/")]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return true
			}
			w.Write([]byte(body))
		case r.Method == http.MethodPost && path == "/works/resolve":
			var body struct{ Query string }
			_ = json.NewDecoder(r.Body).Decode(&body)
			out, ok := f.resolve[body.Query]
			if !ok {
				out = `{"kind":"doi"}`
			}
			w.Write([]byte(out))
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/sections"):
			f.sectionSeq++
			n := f.sectionSeq
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"id":"s-new-%d","title":"t","position":%d}`, n, n)
		case r.Method == http.MethodPatch && strings.Contains(path, "/sections/"):
			w.Write([]byte(`{}`))
		case r.Method == http.MethodPatch && strings.Contains(path, "/items/work/"):
			w.Write([]byte(`{}`))
		case r.Method == http.MethodPost && path == "/collections":
			if f.oldServer {
				w.Write([]byte(`{"id":"coll-new"}`))
				return true
			}
			// The recorder has already read the body; every manifest here uses
			// the key sh/an, which the create must echo back.
			key := f.createKey
			if key == "" {
				key = "sh/an"
			}
			fmt.Fprintf(w, `{"id":"coll-new","import_key":%q}`, key)
		case r.Method == http.MethodDelete && strings.HasPrefix(path, "/collections/"):
			f.deleted = append(f.deleted, strings.TrimPrefix(path, "/collections/"))
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPut && strings.HasSuffix(path, "/items/order"):
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			f.orderBody = body
			if ids, ok := body["entity_ids"].([]any); ok {
				for _, id := range ids {
					f.reordered = append(f.reordered, id.(string))
				}
			}
			w.Write([]byte(`{}`))
		case r.Method == http.MethodPatch && strings.HasPrefix(path, "/collections/"):
			w.Write([]byte(`{"id":"coll-1"}`))
		case r.Method == http.MethodPost && strings.HasSuffix(path, "/items"):
			w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected call %s %s", r.Method, path)
			w.WriteHeader(http.StatusTeapot)
		}
		return true
	}
}

// capture pulls the recorded write bodies into named fields. The recorder
// keeps POST/PATCH bodies in order; PUT is decoded by the handler itself.
func (f *collectionAPI) capture(rec *recorder) {
	i := 0
	for _, call := range rec.mu {
		method, path, _ := strings.Cut(call, " ")
		if method != http.MethodPost && method != http.MethodPatch {
			continue
		}
		body := rec.bodies[i]
		i++
		switch {
		case method == http.MethodPost && path == "/collections":
			f.created = body
		case method == http.MethodPost && strings.HasSuffix(path, "/sections"):
			f.sectionNew = append(f.sectionNew, body)
		case method == http.MethodPatch && strings.Contains(path, "/sections/"):
			if f.sectionPatch == nil {
				f.sectionPatch = map[string]map[string]any{}
			}
			f.sectionPatch[path[strings.LastIndex(path, "/")+1:]] = body
		case method == http.MethodPatch && strings.Contains(path, "/items/work/"):
			if f.itemPatches == nil {
				f.itemPatches = map[string]map[string]any{}
			}
			f.itemPatches[path[strings.LastIndex(path, "/")+1:]] = body
		case method == http.MethodPatch:
			f.patched = body
		case method == http.MethodPost && strings.HasSuffix(path, "/items"):
			for _, item := range body["items"].([]any) {
				entry := item.(map[string]any)
				f.added = append(f.added, entry["entity_id"].(string))
				f.addedItems = append(f.addedItems, entry)
			}
		}
	}
}

func lecture(key string) string {
	return "---\ntitle: L\ntype: lecture\nimport_key: " + key + "\n---\nBody."
}

// writeCollection lays out a manifest and three work files in one folder.
func writeCollection(t *testing.T, manifest string) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, dir, "lecture-01.md", lecture("sh/an/lecture-01"))
	writeFile(t, dir, "lecture-02.md", lecture("sh/an/lecture-02"))
	writeFile(t, dir, "lecture-03.md", lecture("sh/an/lecture-03"))
	return writeFile(t, dir, "README.md", manifest)
}

const collectionManifest = `---
import_key: sh/an
title: Анализ 1
language: ru
collection_type: course
items:
  - lecture-01.md
  - lecture-02.md
  - lecture-03.md
---
Working notes that are not published.`

func runCollection(t *testing.T, f *collectionAPI, path string, dryRun bool) (collectionResult, *recorder) {
	t.Helper()
	rec := newRecorder(t, f.handler(t))
	result := publishCollection(context.Background(), api.New(rec.server.URL, "tok", "test"), path, dryRun)
	f.capture(rec)
	return result, rec
}

func actions(r collectionResult) []string {
	var out []string
	for _, l := range r.Items {
		out = append(out, l.Action+":"+l.Reason)
	}
	return out
}

func TestCollectionCreatesAndAddsPublishedWorksInOrder(t *testing.T) {
	f := &collectionAPI{pubs: map[string]string{
		"sh/an/lecture-01": `{"id":"p1"}`,
		"sh/an/lecture-03": `{"id":"p3"}`,
	}}
	result, rec := runCollection(t, f, writeCollection(t, collectionManifest), false)

	if result.Action != actionCreate || result.CollectionID != "coll-new" {
		t.Fatalf("result = %+v", result)
	}
	if f.created["import_key"] != "sh/an" || f.created["collection_type"] != "course" ||
		f.created["primary_language"] != "ru" {
		t.Errorf("create body = %v", f.created)
	}
	if _, ok := f.created["description"]; ok {
		t.Errorf("the manifest body is working notes and must not become the description: %v", f.created)
	}
	if !slices.Equal(f.added, []string{"p1", "p3"}) {
		t.Errorf("added = %v, want p1 then p3", f.added)
	}
	if want := []string{"add:", "skip:not_published", "add:"}; !slices.Equal(actions(result), want) {
		t.Errorf("items = %v, want %v", actions(result), want)
	}
	if strings.Contains(rec.calls(), "PUT") {
		t.Errorf("a fresh collection is appended in order and needs no reorder: %s", rec.calls())
	}
}

// Find-then-update: an existing collection is patched, only the missing work is
// added, and the manifest's order is restored.
func TestCollectionUpdateAddsMissingAndRestoresOrder(t *testing.T) {
	f := &collectionAPI{
		pubs: map[string]string{
			"sh/an/lecture-01": `{"id":"p1"}`,
			"sh/an/lecture-02": `{"id":"p2"}`,
			"sh/an/lecture-03": `{"id":"p3"}`,
		},
		coll:  `{"id":"coll-1","import_key":"sh/an"}`,
		items: []string{"p3", "extra", "p1"},
	}
	result, _ := runCollection(t, f, writeCollection(t, collectionManifest), false)

	if result.Action != actionUpdate || result.CollectionID != "coll-1" {
		t.Fatalf("result = %+v", result)
	}
	if _, ok := f.patched["import_key"]; ok {
		t.Errorf("an update must not carry the key: %v", f.patched)
	}
	if !slices.Equal(f.added, []string{"p2"}) {
		t.Errorf("added = %v, want only p2", f.added)
	}
	if !result.Reordered || !slices.Equal(f.reordered, []string{"p1", "extra", "p2", "p3"}) {
		t.Errorf("reordered = %v %v", result.Reordered, f.reordered)
	}
	if result.Unlisted != 1 {
		t.Errorf("unlisted = %d, want 1 (the item the manifest does not name)", result.Unlisted)
	}
	if want := []string{"present:", "add:", "present:"}; !slices.Equal(actions(result), want) {
		t.Errorf("items = %v, want %v", actions(result), want)
	}
}

func TestCollectionInOrderReRunAddsAndReordersNothing(t *testing.T) {
	f := &collectionAPI{
		pubs:  map[string]string{"sh/an/lecture-01": `{"id":"p1"}`, "sh/an/lecture-02": `{"id":"p2"}`},
		coll:  `{"id":"coll-1"}`,
		items: []string{"p1", "p2"},
	}
	result, rec := runCollection(t, f, writeCollection(t, collectionManifest), false)

	if result.Reordered || len(f.added) != 0 {
		t.Errorf("reordered=%v added=%v", result.Reordered, f.added)
	}
	if strings.Contains(rec.calls(), "PUT") || strings.Contains(rec.calls(), "POST") {
		t.Errorf("an unchanged collection should only be patched: %s", rec.calls())
	}
}

func TestCollectionDryRunWritesNothing(t *testing.T) {
	f := &collectionAPI{
		pubs:  map[string]string{"sh/an/lecture-02": `{"id":"p2"}`, "sh/an/lecture-01": `{"id":"p1"}`},
		coll:  `{"id":"coll-1"}`,
		items: []string{"p2"},
	}
	result, rec := runCollection(t, f, writeCollection(t, collectionManifest), true)

	if rec.writeSum != 0 || strings.Contains(rec.calls(), "PUT") {
		t.Errorf("--dry-run wrote: %s", rec.calls())
	}
	// The plan still says what the real run would do.
	if !result.Reordered {
		t.Error("dry run should report the reorder the real run would make")
	}
	if want := []string{"add:", "present:", "skip:not_published"}; !slices.Equal(actions(result), want) {
		t.Errorf("items = %v, want %v", actions(result), want)
	}
}

// A deleted or draft work is never added: readers could not open it.
func TestCollectionSkipsDeletedAndDraftWorks(t *testing.T) {
	f := &collectionAPI{pubs: map[string]string{
		"sh/an/lecture-01": `{"id":"p1","removed":true}`,
		"sh/an/lecture-02": `{"id":"p2","is_draft":true}`,
		"sh/an/lecture-03": `{"id":"p3"}`,
	}}
	result, _ := runCollection(t, f, writeCollection(t, collectionManifest), false)

	if !slices.Equal(f.added, []string{"p3"}) {
		t.Errorf("added = %v, want only p3", f.added)
	}
	if want := []string{"skip:deleted", "skip:draft", "add:"}; !slices.Equal(actions(result), want) {
		t.Errorf("items = %v, want %v", actions(result), want)
	}
}

// Against a server that predates collection import keys, every run would make
// a new collection. The create is rolled back and the run fails instead.
func TestCollectionRollsBackOnAServerWithoutImportKeys(t *testing.T) {
	f := &collectionAPI{oldServer: true, pubs: map[string]string{"sh/an/lecture-01": `{"id":"p1"}`}}
	result, _ := runCollection(t, f, writeCollection(t, collectionManifest), false)

	if result.Action != actionFailed || result.Code != "unsupported_server" {
		t.Fatalf("result = %+v", result)
	}
	if !slices.Equal(f.deleted, []string{"coll-new"}) {
		t.Errorf("deleted = %v, want the collection just created", f.deleted)
	}
	if len(f.added) != 0 || result.CollectionID != "" {
		t.Errorf("added=%v collection_id=%q, want nothing kept", f.added, result.CollectionID)
	}
}

// A work deleted after it was added stays in the collection, is reported as
// skipped, and is not miscounted as an item the manifest does not name.
func TestCollectionDeletedWorkAlreadyInTheCollectionIsNotUnlisted(t *testing.T) {
	f := &collectionAPI{
		pubs: map[string]string{
			"sh/an/lecture-01": `{"id":"p1"}`,
			"sh/an/lecture-02": `{"id":"p2","removed":true}`,
		},
		coll:  `{"id":"coll-1"}`,
		items: []string{"p1", "p2"},
	}
	result, rec := runCollection(t, f, writeCollection(t, collectionManifest), false)

	if result.Unlisted != 0 || result.Reordered {
		t.Errorf("unlisted=%d reordered=%v, want 0 and false", result.Unlisted, result.Reordered)
	}
	if strings.Contains(rec.calls(), "DELETE") {
		t.Errorf("lt collection publish never removes an item: %s", rec.calls())
	}
	if want := []string{"present:", "skip:deleted", "skip:not_published"}; !slices.Equal(actions(result), want) {
		t.Errorf("items = %v, want %v", actions(result), want)
	}
}

// Every listed work resolves before anything is written.
func TestCollectionMissingItemFileFailsBeforeAnyRequest(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "lecture-01.md", lecture("sh/an/lecture-01"))
	path := writeFile(t, dir, "README.md", "---\nimport_key: sh/an\ntitle: T\nlanguage: ru\ncollection_type: course\nitems: [lecture-01.md, lecture-02.md]\n---\n")

	result, rec := runCollection(t, &collectionAPI{}, path, false)

	if result.Action != actionFailed || !strings.Contains(result.Message, "lecture-02.md") {
		t.Fatalf("result = %+v", result)
	}
	if len(rec.mu) != 0 {
		t.Errorf("a broken manifest still hit the API: %s", rec.calls())
	}
}

func TestCollectionManifestValidation(t *testing.T) {
	cases := map[string]struct{ manifest, mention string }{
		"no key":      {"---\ntitle: T\nlanguage: ru\ncollection_type: course\n---\n", "import_key"},
		"no type":     {"---\nimport_key: k\ntitle: T\nlanguage: ru\n---\n", "collection_type"},
		"bad type":    {"---\nimport_key: k\ntitle: T\nlanguage: ru\ncollection_type: syllabus\n---\n", "syllabus"},
		"no title":    {"---\nimport_key: k\nlanguage: ru\ncollection_type: course\n---\n", "title"},
		"no language": {"---\nimport_key: k\ntitle: T\ncollection_type: course\n---\n", "language"},
		"publication visibility": {
			"---\nimport_key: k\ntitle: T\nlanguage: ru\ncollection_type: course\nvisibility: private\n---\n", "private"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeFile(t, t.TempDir(), "README.md", c.manifest)
			result, rec := runCollection(t, &collectionAPI{}, path, false)
			if result.Action != actionFailed || result.Code != "validation_error" {
				t.Fatalf("result = %+v", result)
			}
			if !strings.Contains(result.Message, c.mention) {
				t.Errorf("message %q should mention %q", result.Message, c.mention)
			}
			if len(rec.mu) != 0 {
				t.Errorf("an invalid manifest hit the API: %s", rec.calls())
			}
		})
	}
}

func TestCollectionRefusesTwoItemsWithOneKey(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.md", lecture("same"))
	writeFile(t, dir, "b.md", lecture("same"))
	path := writeFile(t, dir, "README.md", "---\nimport_key: k\ntitle: T\nlanguage: ru\ncollection_type: course\nitems: [a.md, b.md]\n---\n")

	result, _ := runCollection(t, &collectionAPI{}, path, false)
	if result.Action != actionFailed || !strings.Contains(result.Message, "same") {
		t.Fatalf("result = %+v", result)
	}
}

// Item paths are relative to the manifest, not to the working directory.
func TestCollectionResolvesItemsRelativeToTheManifest(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, filepath.Join("sh", "an", "lecture-01.md"), lecture("sh/an/lecture-01"))
	path := writeFile(t, root, filepath.Join("sh", "an", "README.md"),
		"---\nimport_key: sh/an\ntitle: T\nlanguage: ru\ncollection_type: course\nitems: [lecture-01.md]\n---\n")

	f := &collectionAPI{pubs: map[string]string{"sh/an/lecture-01": `{"id":"p1"}`}}
	result, _ := runCollection(t, f, path, false)
	if result.Action != actionCreate || !slices.Equal(f.added, []string{"p1"}) {
		t.Fatalf("result = %+v, added = %v", result, f.added)
	}
}

// The kind is the manifest's, not the command's: a plain collection is created
// as one.
func TestCollectionTypeComesFromTheManifest(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.md", lecture("sh/an/lecture-01"))
	path := writeFile(t, dir, "README.md",
		"---\nimport_key: sh/an\ntitle: T\nlanguage: ru\ncollection_type: collection\nitems: [a.md]\n---\n")

	f := &collectionAPI{pubs: map[string]string{"sh/an/lecture-01": `{"id":"p1"}`}}
	result, _ := runCollection(t, f, path, false)
	if result.Action != actionCreate || f.created["collection_type"] != "collection" {
		t.Fatalf("result = %+v, create body = %v", result, f.created)
	}
}
