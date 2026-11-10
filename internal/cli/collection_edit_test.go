package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const collUUID = "c0c0c0c0-0000-4000-8000-0000000000c1"

// runLt runs one lt command against the collection fake, signed in.
func runLt(t *testing.T, f *collectionAPI, args ...string) (string, error, []string) {
	t.Helper()
	var calls []string
	handle := f.handler(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		calls = append(calls, r.Method+" "+r.URL.Path+" "+string(body))
		r.Body = io.NopCloser(bytes.NewReader(body))
		handle(w, r)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LIBRETIMES_TOKEN", "tok")

	root, e := NewRootCommand()
	var out, errOut bytes.Buffer
	e.out, e.err = &out, &errOut
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(append([]string{"--api-url", srv.URL, "--json"}, args...))
	err := root.Execute()
	return out.String(), err, calls
}

func listEditAPI() *collectionAPI {
	f := sectionsAPI()
	f.items = []string{work1}
	f.resolve = map[string]string{
		"https://doi.org/10.1007/978-3-642-29514-0": `{"kind":"doi","indexed":{"id":"` + work2 + `","title":"Morel"}}`,
	}
	f.byKey = map[string]string{"book:fulton-intersection-theory": `{"id":"` + work3 + `","import_key":"book:fulton-intersection-theory","title":"Fulton","type":"book"}`}
	return f
}

func TestCreateMakesAnEmptyListWithItsCategories(t *testing.T) {
	f := &collectionAPI{createKey: "me/algebra"}
	out, err, calls := runLt(t, f, "collection", "create", "--kind", "reading_list", "--title", "Algebra",
		"--language", "ru", "--category", "algebra", "--category", "linear-algebra", "--import-key", "me/algebra", "--yes")
	if err != nil {
		t.Fatalf("err = %v calls = %v", err, calls)
	}
	var created map[string]any
	for _, c := range calls {
		if strings.HasPrefix(c, "POST /collections ") {
			json.Unmarshal([]byte(strings.TrimPrefix(c, "POST /collections ")), &created)
		}
	}
	if created["collection_type"] != "reading_list" || created["primary_language"] != "ru" ||
		len(created["categories"].([]any)) != 2 || created["import_key"] != "me/algebra" {
		t.Errorf("create body = %v", created)
	}
	if !strings.Contains(out, `"collection_id": "coll-new"`) {
		t.Errorf("out = %s", out)
	}
}

func TestCreateNeedsYesAndAKnownKind(t *testing.T) {
	if _, err, calls := runLt(t, &collectionAPI{}, "collection", "create", "--kind", "reading_list", "--title", "A"); err == nil || len(calls) != 0 {
		t.Errorf("no --yes: err = %v calls = %v", err, calls)
	}
	if _, err, calls := runLt(t, &collectionAPI{}, "collection", "create", "--kind", "syllabus", "--title", "A", "--yes"); err == nil || len(calls) != 0 {
		t.Errorf("bad kind: err = %v calls = %v", err, calls)
	}
}

func TestAddByDOIAndKeyIntoANewSectionAndMovesAHeldWork(t *testing.T) {
	f := listEditAPI()
	out, err, calls := runLt(t, f, "collection", "add", collUUID, "https://doi.org/10.1007/978-3-642-29514-0",
		"book:fulton-intersection-theory", work1, "--section", "Further", "--yes")
	if err != nil {
		t.Fatalf("err = %v out = %s calls = %v", err, out, calls)
	}
	joined := strings.Join(calls, "\n")
	if !strings.Contains(joined, "POST /collections/"+collUUID+`/sections {"title":"Further"}`) {
		t.Errorf("no section made: %s", joined)
	}
	if !strings.Contains(joined, `"entity_id":"`+work2+`"`) || !strings.Contains(joined, `"entity_id":"`+work3+`","entity_type":"work","section_id":"s-new-1"`) {
		t.Errorf("adds: %s", joined)
	}
	// work1 was in the preamble: it moves to the end of the new section.
	if f.orderBody == nil || !strings.Contains(joined, `"section_id":"s-new-1","entity_ids":["`+work1+`"]`) {
		t.Errorf("order: %v", f.orderBody)
	}
}

func TestAddWritesNothingWhenAnItemIsNotHeld(t *testing.T) {
	f := listEditAPI()
	_, err, calls := runLt(t, f, "collection", "add", collUUID, "book:fulton-intersection-theory", "10.9999/nobody", "--yes")
	if err == nil {
		t.Fatal("an unheld item must fail the command")
	}
	for _, c := range calls {
		if !strings.HasPrefix(c, "GET ") && !strings.HasPrefix(c, "POST /works/resolve") {
			t.Errorf("wrote: %s", c)
		}
	}
}

func TestAddPinsAnEditionByISBNAndSetsANote(t *testing.T) {
	f := listEditAPI()
	f.expressions = map[string]string{work1: `[{"id":"` + exprA + `","language":"en","year":1970,"isbn13":"9780306406157"}]`}
	_, err, calls := runLt(t, f, "collection", "add", collUUID, work1, "--edition", "0-306-40615-2", "--note", "Ch. 1–3", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(calls, "\n")
	if !strings.Contains(joined, "PATCH /collections/"+collUUID+"/items/work/"+work1+` {"expression_id":"`+exprA+`","note":"Ch. 1–3"}`) {
		t.Errorf("patch: %s", joined)
	}
}

func TestRmRemovesWhatIsThereAndReportsWhatIsNot(t *testing.T) {
	f := listEditAPI()
	out, err, _ := runLt(t, f, "collection", "rm", collUUID, work1, "book:fulton-intersection-theory", "--yes")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.deleted) != 1 || f.deleted[0] != collUUID+"/items/work/"+work1 {
		t.Errorf("deleted = %v", f.deleted)
	}
	if !strings.Contains(out, `"action": "remove"`) || !strings.Contains(out, `"action": "absent"`) {
		t.Errorf("out = %s", out)
	}
}

func TestSectionAddRenameRm(t *testing.T) {
	f := listEditAPI()
	f.sections = []map[string]any{{"id": "s1", "title": "Basics", "position": 0}}
	if _, err, calls := runLt(t, f, "collection", "section", "rename", collUUID, "Basics", "Foundations", "--yes"); err != nil ||
		!strings.Contains(strings.Join(calls, "\n"), "PATCH /collections/"+collUUID+`/sections/s1 {"title":"Foundations"}`) {
		t.Errorf("rename: %v %v", err, calls)
	}
	if out, err, _ := runLt(t, f, "collection", "section", "add", collUUID, "Basics", "--yes"); err != nil || !strings.Contains(out, `"present"`) {
		t.Errorf("add present: %v %s", err, out)
	}
	if _, err, _ := runLt(t, f, "collection", "section", "rm", collUUID, "Basics", "--yes"); err != nil || len(f.deleted) != 1 || f.deleted[0] != collUUID+"/sections/s1" {
		t.Errorf("rm: %v %v", err, f.deleted)
	}
}
