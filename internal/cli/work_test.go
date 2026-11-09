package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/The-LibreTimes/libretimes-cli/internal/api"
)

const workID = "0190aaaa-0000-7000-8000-000000000001"

// workAPI fakes the API slice `lt work attachments` and `lt work translations`
// touch, plus the storage host the API's signed URLs point at. The two are
// separate servers because the property worth testing is that the token goes
// to one and never to the other.
type workAPI struct {
	t  *testing.T
	mu sync.Mutex

	language    string
	faceID      string
	attachments []api.Attachment // what the work has; download_url filled in
	stored      map[string][]byte

	translations map[string]map[string]string

	calls    []string
	uploads  []string // file names that reached storage
	bound    [][]string
	puts     []map[string]any
	nextID   int
	apiSrv   *httptest.Server
	storeSrv *httptest.Server
}

func newWorkAPI(t *testing.T) *workAPI {
	f := &workAPI{t: t, language: "ru", stored: map[string][]byte{},
		translations: map[string]map[string]string{}}
	f.storeSrv = httptest.NewServer(http.HandlerFunc(f.storage))
	f.apiSrv = httptest.NewServer(http.HandlerFunc(f.api))
	t.Cleanup(f.storeSrv.Close)
	t.Cleanup(f.apiSrv.Close)
	return f
}

func (f *workAPI) client() *api.Client { return api.New(f.apiSrv.URL, "tok", "test") }

// attach seeds a file the work already has.
func (f *workAPI) attach(id, name string, data []byte) {
	f.stored[id] = data
	f.attachments = append(f.attachments, api.Attachment{ID: id, OriginalName: name,
		SizeBytes: int64(len(data)), Status: "active"})
}

func (f *workAPI) ids() []string {
	var out []string
	for _, a := range f.attachments {
		out = append(out, a.ID)
	}
	return out
}

func (f *workAPI) storage(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "" {
		f.t.Errorf("the bearer token reached the storage host: %s %s", r.Method, r.URL.Path)
	}
	switch r.Method {
	case http.MethodGet:
		data, ok := f.stored[strings.TrimPrefix(r.URL.Path, "/obj/")]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Write(data)
	case http.MethodPost:
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			f.t.Fatal(err)
		}
		if r.FormValue("policy") != "signed" {
			f.t.Errorf("the signed policy field did not ride with the file")
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			f.t.Fatal(err)
		}
		data, _ := io.ReadAll(file)
		f.mu.Lock()
		f.uploads = append(f.uploads, header.Filename)
		f.stored["pending-"+header.Filename] = data
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}
}

func (f *workAPI) api(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	if r.Header.Get("Authorization") != "Bearer tok" {
		f.t.Errorf("an API call went without the token: %s %s", r.Method, r.URL.Path)
	}
	path := r.URL.Path
	transPrefix := "/works/" + workID + "/texts"
	switch {
	case r.Method == http.MethodGet && path == "/works/"+workID:
		atts := make([]api.Attachment, len(f.attachments))
		for i, a := range f.attachments {
			a.DownloadURL = f.storeSrv.URL + "/obj/" + a.ID
			atts[i] = a
		}
		json.NewEncoder(w).Encode(map[string]any{
			"id": workID, "title": "Sheet", "type": "lecture-speech", "kind": "problems", "language": f.language,
			"body_attachment_id": f.faceID, "attachment_ids": f.ids(), "attachments": atts,
		})
	case r.Method == http.MethodPatch && path == "/works/"+workID:
		var body struct {
			AttachmentIDs []string `json:"attachment_ids"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		f.bound = append(f.bound, body.AttachmentIDs)
		w.Write([]byte(`{"id":"` + workID + `"}`))
	case r.Method == http.MethodPost && path == "/attachments/request-upload":
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		f.nextID++
		id := fmt.Sprintf("new-%d", f.nextID)
		json.NewEncoder(w).Encode(map[string]any{
			"attachment_id": id, "upload_url": f.storeSrv.URL + "/upload",
			"upload_fields": map[string]string{"policy": "signed"}, "upload_token": "ut-" + id,
		})
	case r.Method == http.MethodPost && strings.HasPrefix(path, "/attachments/") && strings.HasSuffix(path, "/confirm"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, "/attachments/"), "/confirm")
		json.NewEncoder(w).Encode(map[string]any{"id": id, "status": "active"})
	case r.Method == http.MethodGet && strings.HasPrefix(path, transPrefix+"/"):
		lang := strings.TrimPrefix(path, transPrefix+"/")
		tr, ok := f.translations[lang]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		out := map[string]any{"language": lang}
		for k, v := range tr {
			out[k] = v
		}
		json.NewEncoder(w).Encode(out)
	case r.Method == http.MethodPatch && strings.HasPrefix(path, transPrefix+"/"):
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		f.puts = append(f.puts, body)
		lang := strings.TrimPrefix(path, transPrefix+"/")
		body["language"] = lang
		json.NewEncoder(w).Encode(body)
	default:
		f.t.Errorf("unexpected call %s %s", r.Method, path)
		w.WriteHeader(http.StatusTeapot)
	}
}

func actionsOf(r attachmentsResult) []string {
	var out []string
	for _, f := range r.Files {
		out = append(out, f.Name+":"+f.Action)
	}
	return out
}

// A new file is uploaded and appended; the files already there keep their
// places. The token goes to the API and never to storage.
func TestAttachAddsAndKeepsWhatIsThere(t *testing.T) {
	f := newWorkAPI(t)
	f.attach("old-1", "data.csv", []byte("a,b\n"))
	path := writeFile(t, t.TempDir(), "sheet.ipynb", `{"cells":[]}`)

	r, err := addAttachments(context.Background(), f.client(), workID, []string{path}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"sheet.ipynb:create"}; !slices.Equal(actionsOf(r), want) {
		t.Fatalf("actions = %v, want %v", actionsOf(r), want)
	}
	if !slices.Equal(f.uploads, []string{"sheet.ipynb"}) {
		t.Errorf("uploads = %v", f.uploads)
	}
	if len(f.bound) != 1 || !slices.Equal(f.bound[0], []string{"old-1", "new-1"}) {
		t.Errorf("bound = %v, want [old-1 new-1]", f.bound)
	}
}

// Re-running with the same bytes is a no-op: compared by content, nothing
// uploaded, nothing rebound.
func TestAttachSameBytesIsUnchanged(t *testing.T) {
	f := newWorkAPI(t)
	f.attach("old-1", "sheet.ipynb", []byte(`{"cells":[]}`))
	path := writeFile(t, t.TempDir(), "sheet.ipynb", `{"cells":[]}`)

	r, _ := addAttachments(context.Background(), f.client(), workID, []string{path}, false, false)
	if want := []string{"sheet.ipynb:unchanged"}; !slices.Equal(actionsOf(r), want) {
		t.Fatalf("actions = %v", actionsOf(r))
	}
	if len(f.uploads) != 0 || len(f.bound) != 0 {
		t.Errorf("an unchanged file was written: uploads=%v bound=%v", f.uploads, f.bound)
	}
}

// Same size, different bytes: the size check alone would call this unchanged.
func TestAttachDifferentBytesIsBlockedWithoutReplace(t *testing.T) {
	f := newWorkAPI(t)
	f.attach("old-1", "sheet.ipynb", []byte(`{"cells":[1]}`))
	path := writeFile(t, t.TempDir(), "sheet.ipynb", `{"cells":[2]}`)

	r, _ := addAttachments(context.Background(), f.client(), workID, []string{path}, false, false)
	if r.Files[0].Action != actionBlocked || r.Files[0].Code != "attachment_differs" {
		t.Fatalf("result = %+v", r.Files[0])
	}
	if len(f.uploads) != 0 || len(f.bound) != 0 {
		t.Errorf("a blocked file was written: uploads=%v bound=%v", f.uploads, f.bound)
	}
}

// --replace swaps the file in its own place in the list.
func TestAttachReplaceSwapsInPlace(t *testing.T) {
	f := newWorkAPI(t)
	f.attach("old-1", "sheet.ipynb", []byte(`v1`))
	f.attach("old-2", "data.csv", []byte(`x`))
	path := writeFile(t, t.TempDir(), "sheet.ipynb", `v2-longer`)

	r, _ := addAttachments(context.Background(), f.client(), workID, []string{path}, true, false)
	if r.Files[0].Action != attachReplace || r.Files[0].Replaces != "old-1" {
		t.Fatalf("result = %+v", r.Files[0])
	}
	if len(f.bound) != 1 || !slices.Equal(f.bound[0], []string{"new-1", "old-2"}) {
		t.Errorf("bound = %v, want [new-1 old-2]", f.bound)
	}
}

// The work's own file view is its text, not an attachment to swap.
func TestAttachRefusesTheFileFace(t *testing.T) {
	f := newWorkAPI(t)
	f.attach("face", "sheet.pdf", []byte(`%PDF-1`))
	f.faceID = "face"
	path := writeFile(t, t.TempDir(), "sheet.pdf", `%PDF-2`)

	r, _ := addAttachments(context.Background(), f.client(), workID, []string{path}, true, false)
	if r.Files[0].Action != actionBlocked || r.Files[0].Code != "file_face" {
		t.Fatalf("result = %+v", r.Files[0])
	}
	if len(f.uploads) != 0 || len(f.bound) != 0 {
		t.Errorf("the file face was written: uploads=%v bound=%v", f.uploads, f.bound)
	}
}

func TestAttachDryRunWritesNothing(t *testing.T) {
	f := newWorkAPI(t)
	path := writeFile(t, t.TempDir(), "sheet.ipynb", `{}`)

	r, _ := addAttachments(context.Background(), f.client(), workID, []string{path}, false, true)
	if want := []string{"sheet.ipynb:create"}; !slices.Equal(actionsOf(r), want) {
		t.Fatalf("actions = %v", actionsOf(r))
	}
	for _, c := range f.calls {
		if !strings.HasPrefix(c, "GET ") {
			t.Errorf("--dry-run wrote: %v", f.calls)
		}
	}
	if len(f.uploads) != 0 {
		t.Errorf("--dry-run uploaded: %v", f.uploads)
	}
}

func TestAttachRefusesTwoFilesWithOneName(t *testing.T) {
	a := writeFile(t, t.TempDir(), "sheet.ipynb", `1`)
	b := writeFile(t, t.TempDir(), "sheet.ipynb", `2`)
	if err := uniqueNames([]string{a, b}); err == nil {
		t.Fatal("two files named sheet.ipynb were accepted")
	}
}

func TestDetachByNameKeepsTheRest(t *testing.T) {
	f := newWorkAPI(t)
	f.attach("a", "one.txt", []byte(`1`))
	f.attach("b", "two.txt", []byte(`2`))
	f.attach("face", "text.pdf", []byte(`%PDF`))
	f.faceID = "face"

	r, err := removeAttachments(context.Background(), f.client(), workID,
		[]string{"one.txt", "face", "nope.txt"}, false)
	if err != nil {
		t.Fatal(err)
	}
	// Each line names what was asked for; the face was asked for by id.
	if want := []string{"one.txt:remove", "face:blocked", "nope.txt:failed"}; !slices.Equal(actionsOf(r), want) {
		t.Errorf("actions = %v, want %v", actionsOf(r), want)
	}
	if len(f.bound) != 1 || !slices.Equal(f.bound[0], []string{"b", "face"}) {
		t.Errorf("bound = %v, want [b face]", f.bound)
	}
}

const enSheet = `---
language: en
title: Sheet 3. Green's theorem
description: A contour integral as a double integral.
---

::::problem[One]{#p-1 number="1"}
Compute.
::::
`

func TestTranslationCreatesThenIsUnchanged(t *testing.T) {
	f := newWorkAPI(t)
	path := writeFile(t, t.TempDir(), "sheet.en.md", enSheet)

	r := setTranslation(context.Background(), f.client(), workTranslations, workID, "ru", path, false, map[string]string{})
	if r.Action != translationCreate || r.Language != "en" {
		t.Fatalf("result = %+v", r)
	}
	if len(f.puts) != 1 || f.puts[0]["title"] != "Sheet 3. Green's theorem" ||
		!strings.HasPrefix(f.puts[0]["content"].(string), "::::problem[One]") {
		t.Fatalf("put = %v", f.puts)
	}

	// What the site now holds, CRLF and trailing space included: still the
	// same text.
	f.translations["en"] = map[string]string{
		"title":       "Sheet 3. Green's theorem",
		"description": "A contour integral as a double integral.",
		"content":     "::::problem[One]{#p-1 number=\"1\"}\r\nCompute.\r\n::::\r\n  ",
	}
	r = setTranslation(context.Background(), f.client(), workTranslations, workID, "ru", path, false, map[string]string{})
	if r.Action != translationUnchanged || len(f.puts) != 1 {
		t.Fatalf("result = %+v, puts = %d", r, len(f.puts))
	}
}

func TestTranslationUpdateNamesTheChangedFields(t *testing.T) {
	f := newWorkAPI(t)
	f.translations["en"] = map[string]string{"title": "Old", "description": "A contour integral as a double integral.",
		"content": "::::problem[One]{#p-1 number=\"1\"}\nCompute.\n::::"}
	path := writeFile(t, t.TempDir(), "sheet.en.md", enSheet)

	r := setTranslation(context.Background(), f.client(), workTranslations, workID, "ru", path, true, map[string]string{})
	if r.Action != translationUpdate || !slices.Equal(r.Changes, []string{"title"}) {
		t.Fatalf("result = %+v", r)
	}
	if len(f.puts) != 0 {
		t.Errorf("--dry-run wrote: %v", f.puts)
	}
}

func TestTranslationRefusesTheOriginalLanguage(t *testing.T) {
	f := newWorkAPI(t)
	path := writeFile(t, t.TempDir(), "sheet.ru.md", strings.Replace(enSheet, "language: en", "language: ru", 1))

	r := setTranslation(context.Background(), f.client(), workTranslations, workID, "ru", path, false, map[string]string{})
	if r.Action != actionFailed || r.Code != "primary_language" {
		t.Fatalf("result = %+v", r)
	}
	if len(f.calls) != 0 {
		t.Errorf("a refused file hit the API: %v", f.calls)
	}
}

func TestTranslationFileValidation(t *testing.T) {
	cases := map[string]struct {
		subject translationSubject
		file    string
		mention string
	}{
		"no language":          {workTranslations, "---\ntitle: T\n---\nBody", "language"},
		"no title":             {workTranslations, "---\nlanguage: en\n---\nBody", "title"},
		"work without body":    {workTranslations, "---\nlanguage: en\ntitle: T\n---\n", "body"},
		"collection with body": {collectionTranslations, "---\nlanguage: en\ntitle: T\n---\nBody", "body"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newWorkAPI(t)
			path := writeFile(t, t.TempDir(), "x.md", c.file)
			r := setTranslation(context.Background(), f.client(), c.subject, workID, "ru", path, false, map[string]string{})
			if r.Action != actionFailed || !strings.Contains(r.Message, c.mention) {
				t.Fatalf("result = %+v", r)
			}
			if len(f.calls) != 0 {
				t.Errorf("an invalid file hit the API: %v", f.calls)
			}
		})
	}
}

func TestTranslationTwoFilesForOneLanguage(t *testing.T) {
	f := newWorkAPI(t)
	dir := t.TempDir()
	a := writeFile(t, dir, "a.en.md", enSheet)
	b := writeFile(t, dir, "b.en.md", enSheet)
	seen := map[string]string{}
	setTranslation(context.Background(), f.client(), workTranslations, workID, "ru", a, true, seen)
	r := setTranslation(context.Background(), f.client(), workTranslations, workID, "ru", b, true, seen)
	if r.Action != actionFailed || !strings.Contains(r.Message, "a.en.md") {
		t.Fatalf("result = %+v", r)
	}
}

func TestIDFromArg(t *testing.T) {
	cases := map[string]string{
		workID:                                  workID,
		strings.ToUpper(workID):                 workID,
		"https://libretimes.io/works/" + workID: workID,
		"https://libretimes.io/works/" + workID + "#p-3":          workID,
		"https://libretimes.io/publications/" + workID + "/pdf":   workID,
		"https://api.libretimes.io/libretimes/v1/works/" + workID: workID,
		"https://libretimes.io/@ada/" + workID:                    "",
		"works/not-an-id":                                         "",
	}
	for arg, want := range cases {
		got, ok := idFromArg(arg, "works", "publications")
		if got != want || ok != (want != "") {
			t.Errorf("idFromArg(%q) = %q, %v; want %q", arg, got, ok, want)
		}
	}
}

// The file a work was published from is the handle for it afterwards.
func TestResolveWorkFromItsFile(t *testing.T) {
	rec := newRecorder(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Query().Get("key") != "va/03-green" {
			t.Errorf("looked up key %q", r.URL.Query().Get("key"))
		}
		w.Write([]byte(`{"id":"` + strings.ToUpper(workID) + `"}`))
		return true
	})
	path := writeFile(t, t.TempDir(), "03-green.md", "---\ntype: problems\nimport_key: va/03-green\n---\nBody")
	id, err := resolveWork(context.Background(), api.New(rec.server.URL, "tok", "test"), path)
	if err != nil || id != workID {
		t.Fatalf("id = %q, err = %v", id, err)
	}
}

func TestResolveWorkNotPublished(t *testing.T) {
	rec := newRecorder(t, func(w http.ResponseWriter, r *http.Request) bool {
		w.WriteHeader(http.StatusNotFound)
		return true
	})
	path := writeFile(t, t.TempDir(), "x.md", "---\ntype: problems\nimport_key: k\n---\nBody")
	_, err := resolveWork(context.Background(), api.New(rec.server.URL, "tok", "test"), path)
	var apiErr *api.Error
	if err == nil || !strings.Contains(err.Error(), "lt work publish") {
		t.Fatalf("err = %v", err)
	}
	if e, ok := err.(*api.Error); ok {
		apiErr = e
	}
	if apiErr == nil || apiErr.Code != codeNotPublished {
		t.Errorf("code = %v, want %s", apiErr, codeNotPublished)
	}
}

// runLT runs the real command tree against a fake API.
func runLT(t *testing.T, apiURL string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	t.Setenv("LIBRETIMES_TOKEN", "tok")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root, e := NewRootCommand()
	var out, errb bytes.Buffer
	e.out, e.err = &out, &errb
	root.SetArgs(append([]string{"--api-url", apiURL}, args...))
	root.SetOut(&out)
	root.SetErr(&errb)
	err := root.ExecuteContext(context.Background())
	if err == nil {
		return out.String(), errb.String(), ExitOK
	}
	_, msg, exit := classify(err)
	return out.String(), errb.String() + msg, exit
}

// The tree is nouns first: the old verbs are gone, not aliased.
func TestCommandTree(t *testing.T) {
	root, _ := NewRootCommand()
	have := map[string]bool{}
	for _, c := range root.Commands() {
		have[c.Name()] = true
		for _, sub := range c.Commands() {
			have[c.Name()+" "+sub.Name()] = true
			for _, leaf := range sub.Commands() {
				have[c.Name()+" "+sub.Name()+" "+leaf.Name()] = true
			}
		}
	}
	for _, want := range []string{
		"work publish", "work ls", "work show", "work edit",
		"work notes", "work suggest", "work review", "work accept", "work reject",
		"work attachments add", "work attachments ls", "work attachments rm",
		"work translations set", "work translations ls", "work translations rm",
		"collection publish", "collection ls", "collection show",
		"collection translations set", "collection translations ls", "collection translations rm",
		"auth login", "whoami", "version",
		"profile image show", "profile image set", "profile image rm",
	} {
		if !have[want] {
			t.Errorf("missing `lt %s`", want)
		}
	}
	for _, gone := range []string{"publish", "book", "course"} {
		if have[gone] {
			t.Errorf("`lt %s` should be gone", gone)
		}
	}
}

func TestWorkPublishRefusesPruneOnANonBook(t *testing.T) {
	rec := newRecorder(t, nil)
	path := writeFile(t, t.TempDir(), "note.md", "---\ntitle: N\ntype: lecture\nimport_key: k\n---\nBody")
	_, stderr, code := runLT(t, rec.server.URL, "work", "publish", path, "--prune", "--dry-run")
	if code != ExitAPIError || !strings.Contains(stderr, "--prune is for books") {
		t.Fatalf("code = %d, stderr = %s", code, stderr)
	}
	if len(rec.mu) != 0 {
		t.Errorf("a refused file hit the API: %s", rec.calls())
	}
}

func TestWritesNeedYes(t *testing.T) {
	rec := newRecorder(t, nil)
	path := writeFile(t, t.TempDir(), "note.md", "---\ntitle: N\ntype: lecture\n---\nBody")
	for _, args := range [][]string{
		{"work", "publish", path},
		{"work", "attachments", "add", workID, path},
		{"work", "attachments", "rm", workID, "x"},
		{"work", "translations", "set", workID, path},
		{"collection", "publish", path},
	} {
		_, _, code := runLT(t, rec.server.URL, args...)
		if code != ExitNeedsConfirm {
			t.Errorf("lt %s: exit %d, want %d", strings.Join(args, " "), code, ExitNeedsConfirm)
		}
	}
	if len(rec.mu) != 0 {
		t.Errorf("an unconfirmed write hit the API: %s", rec.calls())
	}
}

func TestIsBookFile(t *testing.T) {
	dir := t.TempDir()
	book := writeFile(t, dir, "book.md", "---\ntype: book\nimport_key: k\ncontents:\n  - file: c1.md\n---\n")
	note := writeFile(t, dir, "note.md", "---\ntype: lecture\n---\nBody")
	if !isBookFile(book) || isBookFile(note) || isBookFile(dir+"/missing.md") {
		t.Errorf("isBookFile: book=%v note=%v missing=%v", isBookFile(book), isBookFile(note),
			isBookFile(dir+"/missing.md"))
	}
}
