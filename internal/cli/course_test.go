package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/The-LibreTimes/libretimes-cli/internal/api"
)

// courseAPI is a fake of the slice of the API `lt course publish` touches.
// Publications are keyed by import_key; the collection exists when coll is set.
type courseAPI struct {
	pubs  map[string]string // import_key -> JSON ref
	coll  string            // JSON ref for the course's import_key, "" = none
	items []string          // entity ids already in the collection, in order

	// oldServer answers a create without echoing import_key, as a server that
	// predates collection import keys does.
	oldServer bool
	deleted   []string

	created   map[string]any
	patched   map[string]any
	added     []string
	reordered []string
}

func (f *courseAPI) handler(t *testing.T) func(w http.ResponseWriter, r *http.Request) bool {
	return func(w http.ResponseWriter, r *http.Request) bool {
		path := r.URL.Path
		switch {
		case r.Method == http.MethodGet && path == "/publications/me/by-import-key":
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
				page["items"] = append(page["items"].([]map[string]any),
					map[string]any{"entity_type": "publication", "entity_id": id, "position": i})
			}
			json.NewEncoder(w).Encode(page)
		case r.Method == http.MethodPost && path == "/collections":
			if f.oldServer {
				w.Write([]byte(`{"id":"coll-new"}`))
				return true
			}
			// The recorder has already read the body; every manifest here uses
			// the key sh/an, which the create must echo back.
			w.Write([]byte(`{"id":"coll-new","import_key":"sh/an"}`))
		case r.Method == http.MethodDelete && strings.HasPrefix(path, "/collections/"):
			f.deleted = append(f.deleted, strings.TrimPrefix(path, "/collections/"))
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPut && strings.HasSuffix(path, "/items/order"):
			var body struct {
				EntityIDs []string `json:"entity_ids"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			f.reordered = body.EntityIDs
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
func (f *courseAPI) capture(rec *recorder) {
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
		case method == http.MethodPatch:
			f.patched = body
		case method == http.MethodPost && strings.HasSuffix(path, "/items"):
			for _, item := range body["items"].([]any) {
				f.added = append(f.added, item.(map[string]any)["entity_id"].(string))
			}
		}
	}
}

func lecture(key string) string {
	return "---\ntitle: L\ntype: lecture\nimport_key: " + key + "\n---\nBody."
}

// writeCourse lays out a manifest and three lecture files in one folder.
func writeCourse(t *testing.T, manifest string) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, dir, "lecture-01.md", lecture("sh/an/lecture-01"))
	writeFile(t, dir, "lecture-02.md", lecture("sh/an/lecture-02"))
	writeFile(t, dir, "lecture-03.md", lecture("sh/an/lecture-03"))
	return writeFile(t, dir, "README.md", manifest)
}

const courseManifest = `---
import_key: sh/an
title: Анализ 1
language: ru
lectures:
  - lecture-01.md
  - lecture-02.md
  - lecture-03.md
---
Working notes that are not published.`

func runCourse(t *testing.T, f *courseAPI, path string, dryRun bool) (courseResult, *recorder) {
	t.Helper()
	rec := newRecorder(t, f.handler(t))
	result := publishCourse(context.Background(), api.New(rec.server.URL, "tok", "test"), path, dryRun)
	f.capture(rec)
	return result, rec
}

func actions(r courseResult) []string {
	var out []string
	for _, l := range r.Lectures {
		out = append(out, l.Action+":"+l.Reason)
	}
	return out
}

func TestCourseCreatesAndAddsPublishedLecturesInOrder(t *testing.T) {
	f := &courseAPI{pubs: map[string]string{
		"sh/an/lecture-01": `{"id":"p1"}`,
		"sh/an/lecture-03": `{"id":"p3"}`,
	}}
	result, rec := runCourse(t, f, writeCourse(t, courseManifest), false)

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
		t.Errorf("lectures = %v, want %v", actions(result), want)
	}
	if strings.Contains(rec.calls(), "PUT") {
		t.Errorf("a fresh course is appended in order and needs no reorder: %s", rec.calls())
	}
}

// Find-then-update: an existing course is patched, only the missing lecture is
// added, and the manifest's order is restored.
func TestCourseUpdateAddsMissingAndRestoresOrder(t *testing.T) {
	f := &courseAPI{
		pubs: map[string]string{
			"sh/an/lecture-01": `{"id":"p1"}`,
			"sh/an/lecture-02": `{"id":"p2"}`,
			"sh/an/lecture-03": `{"id":"p3"}`,
		},
		coll:  `{"id":"coll-1","import_key":"sh/an"}`,
		items: []string{"p3", "extra", "p1"},
	}
	result, _ := runCourse(t, f, writeCourse(t, courseManifest), false)

	if result.Action != actionUpdate || result.CollectionID != "coll-1" {
		t.Fatalf("result = %+v", result)
	}
	if _, ok := f.patched["import_key"]; ok {
		t.Errorf("an update must not carry the key: %v", f.patched)
	}
	if !slices.Equal(f.added, []string{"p2"}) {
		t.Errorf("added = %v, want only p2", f.added)
	}
	if !result.Reordered || !slices.Equal(f.reordered, []string{"p1", "p2", "p3"}) {
		t.Errorf("reordered = %v %v", result.Reordered, f.reordered)
	}
	if result.Unlisted != 1 {
		t.Errorf("unlisted = %d, want 1 (the item the manifest does not name)", result.Unlisted)
	}
	if want := []string{"present:", "add:", "present:"}; !slices.Equal(actions(result), want) {
		t.Errorf("lectures = %v, want %v", actions(result), want)
	}
}

func TestCourseInOrderReRunAddsAndReordersNothing(t *testing.T) {
	f := &courseAPI{
		pubs:  map[string]string{"sh/an/lecture-01": `{"id":"p1"}`, "sh/an/lecture-02": `{"id":"p2"}`},
		coll:  `{"id":"coll-1"}`,
		items: []string{"p1", "p2"},
	}
	result, rec := runCourse(t, f, writeCourse(t, courseManifest), false)

	if result.Reordered || len(f.added) != 0 {
		t.Errorf("reordered=%v added=%v", result.Reordered, f.added)
	}
	if strings.Contains(rec.calls(), "PUT") || strings.Contains(rec.calls(), "POST") {
		t.Errorf("an unchanged course should only be patched: %s", rec.calls())
	}
}

func TestCourseDryRunWritesNothing(t *testing.T) {
	f := &courseAPI{
		pubs:  map[string]string{"sh/an/lecture-02": `{"id":"p2"}`, "sh/an/lecture-01": `{"id":"p1"}`},
		coll:  `{"id":"coll-1"}`,
		items: []string{"p2"},
	}
	result, rec := runCourse(t, f, writeCourse(t, courseManifest), true)

	if rec.writeSum != 0 || strings.Contains(rec.calls(), "PUT") {
		t.Errorf("--dry-run wrote: %s", rec.calls())
	}
	// The plan still says what the real run would do.
	if !result.Reordered {
		t.Error("dry run should report the reorder the real run would make")
	}
	if want := []string{"add:", "present:", "skip:not_published"}; !slices.Equal(actions(result), want) {
		t.Errorf("lectures = %v, want %v", actions(result), want)
	}
}

// A deleted or draft lecture is never added: readers could not open it.
func TestCourseSkipsDeletedAndDraftLectures(t *testing.T) {
	f := &courseAPI{pubs: map[string]string{
		"sh/an/lecture-01": `{"id":"p1","removed":true}`,
		"sh/an/lecture-02": `{"id":"p2","is_draft":true}`,
		"sh/an/lecture-03": `{"id":"p3"}`,
	}}
	result, _ := runCourse(t, f, writeCourse(t, courseManifest), false)

	if !slices.Equal(f.added, []string{"p3"}) {
		t.Errorf("added = %v, want only p3", f.added)
	}
	if want := []string{"skip:deleted", "skip:draft", "add:"}; !slices.Equal(actions(result), want) {
		t.Errorf("lectures = %v, want %v", actions(result), want)
	}
}

// Against a server that predates collection import keys, every run would make
// a new course. The create is rolled back and the run fails instead.
func TestCourseRollsBackOnAServerWithoutImportKeys(t *testing.T) {
	f := &courseAPI{oldServer: true, pubs: map[string]string{"sh/an/lecture-01": `{"id":"p1"}`}}
	result, _ := runCourse(t, f, writeCourse(t, courseManifest), false)

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

// A lecture deleted after it was added stays in the course, is reported as
// skipped, and is not miscounted as an item the manifest does not name.
func TestCourseDeletedLectureAlreadyInTheCourseIsNotUnlisted(t *testing.T) {
	f := &courseAPI{
		pubs: map[string]string{
			"sh/an/lecture-01": `{"id":"p1"}`,
			"sh/an/lecture-02": `{"id":"p2","removed":true}`,
		},
		coll:  `{"id":"coll-1"}`,
		items: []string{"p1", "p2"},
	}
	result, rec := runCourse(t, f, writeCourse(t, courseManifest), false)

	if result.Unlisted != 0 || result.Reordered {
		t.Errorf("unlisted=%d reordered=%v, want 0 and false", result.Unlisted, result.Reordered)
	}
	if strings.Contains(rec.calls(), "DELETE") {
		t.Errorf("lt course never removes an item: %s", rec.calls())
	}
	if want := []string{"present:", "skip:deleted", "skip:not_published"}; !slices.Equal(actions(result), want) {
		t.Errorf("lectures = %v, want %v", actions(result), want)
	}
}

// Every lecture resolves before anything is written.
func TestCourseMissingLectureFileFailsBeforeAnyRequest(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "lecture-01.md", lecture("sh/an/lecture-01"))
	path := writeFile(t, dir, "README.md", "---\nimport_key: sh/an\ntitle: T\nlanguage: ru\nlectures: [lecture-01.md, lecture-02.md]\n---\n")

	result, rec := runCourse(t, &courseAPI{}, path, false)

	if result.Action != actionFailed || !strings.Contains(result.Message, "lecture-02.md") {
		t.Fatalf("result = %+v", result)
	}
	if len(rec.mu) != 0 {
		t.Errorf("a broken manifest still hit the API: %s", rec.calls())
	}
}

func TestCourseManifestValidation(t *testing.T) {
	cases := map[string]struct{ manifest, mention string }{
		"no key":      {"---\ntitle: T\nlanguage: ru\n---\n", "import_key"},
		"no title":    {"---\nimport_key: k\nlanguage: ru\n---\n", "title"},
		"no language": {"---\nimport_key: k\ntitle: T\n---\n", "language"},
		"publication visibility": {
			"---\nimport_key: k\ntitle: T\nlanguage: ru\nvisibility: private\n---\n", "private"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			path := writeFile(t, t.TempDir(), "README.md", c.manifest)
			result, rec := runCourse(t, &courseAPI{}, path, false)
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

func TestCourseRefusesTwoLecturesWithOneKey(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "a.md", lecture("same"))
	writeFile(t, dir, "b.md", lecture("same"))
	path := writeFile(t, dir, "README.md", "---\nimport_key: k\ntitle: T\nlanguage: ru\nlectures: [a.md, b.md]\n---\n")

	result, _ := runCourse(t, &courseAPI{}, path, false)
	if result.Action != actionFailed || !strings.Contains(result.Message, "same") {
		t.Fatalf("result = %+v", result)
	}
}

// Lecture paths are relative to the manifest, not to the working directory.
func TestCourseResolvesLecturesRelativeToTheManifest(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, filepath.Join("sh", "an", "lecture-01.md"), lecture("sh/an/lecture-01"))
	path := writeFile(t, root, filepath.Join("sh", "an", "README.md"),
		"---\nimport_key: sh/an\ntitle: T\nlanguage: ru\nlectures: [lecture-01.md]\n---\n")

	f := &courseAPI{pubs: map[string]string{"sh/an/lecture-01": `{"id":"p1"}`}}
	result, _ := runCourse(t, f, path, false)
	if result.Action != actionCreate || !slices.Equal(f.added, []string{"p1"}) {
		t.Fatalf("result = %+v, added = %v", result, f.added)
	}
}
