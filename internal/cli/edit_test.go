package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

const editSession = "0190aaaa-0000-7000-8000-0000000000e1"

// editAPI fakes the edit-session routes of one work. `refuse` makes the
// changes call answer the way the platform does for an ambiguous `old`.
type editAPI struct {
	mu        sync.Mutex
	calls     []string
	changes   []json.RawMessage
	summary   string
	refuse    bool
	stale     bool
	discarded bool
	srv       *httptest.Server
}

func newEditAPI(t *testing.T) *editAPI {
	f := &editAPI{}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *editAPI) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	base := "/works/" + workID + "/edits"
	f.calls = append(f.calls, r.Method+" "+strings.TrimPrefix(r.URL.Path, base))
	w.Header().Set("Content-Type", "application/json")
	var body map[string]json.RawMessage
	_ = json.NewDecoder(r.Body).Decode(&body)
	switch {
	case r.Method == "POST" && r.URL.Path == base:
		_, _ = w.Write([]byte(`{"id":"` + editSession + `","work_id":"` + workID + `","status":"open"}`))
	case r.URL.Path == base+"/"+editSession+"/changes":
		if f.refuse {
			w.WriteHeader(422)
			_, _ = w.Write([]byte(`{"session":{},"results":[{"index":0,"ok":true},` +
				`{"index":1,"ok":false,"error":"ambiguous","occurrences":3}]}`))
			return
		}
		_ = json.Unmarshal(body["changes"], &f.changes)
		_, _ = w.Write([]byte(`{"session":{},"results":[{"index":0,"ok":true}]}`))
	case r.URL.Path == base+"/"+editSession+"/diff":
		_, _ = w.Write([]byte(`{"targets":[{"language":"ru","field":"content","diff":"-teh\n+the\n"}],"structure":[]}`))
	case r.URL.Path == base+"/"+editSession+"/commit":
		if f.stale {
			w.WriteHeader(409)
			_, _ = w.Write([]byte(`{"error":{"status_code":409,"detail":{"error_code":"STALE_SESSION",` +
				`"message":"The live text changed since this session copied it."}}}`))
			return
		}
		_ = json.Unmarshal(body["summary"], &f.summary)
		_, _ = w.Write([]byte(`{"version_number":5,"awaiting_publish":false}`))
	case r.Method == "DELETE" && r.URL.Path == base+"/"+editSession:
		f.discarded = true
		w.WriteHeader(204)
	default:
		w.WriteHeader(404)
	}
}

func changesFile(t *testing.T, content string) string {
	path := filepath.Join(t.TempDir(), "changes.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const oneChange = `[{"language":"ru","field":"content","old":"teh","new":"the"}]`

func TestEditCommitsOneVersionWithTheSummary(t *testing.T) {
	f := newEditAPI(t)
	out, errOut, code := runLT(t, f.srv.URL, "work", "edit", workID, changesFile(t, oneChange), "-m", "Typo", "--yes")
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	want := []string{"POST ", "POST /" + editSession + "/changes", "GET /" + editSession + "/diff",
		"POST /" + editSession + "/commit"}
	if strings.Join(f.calls, "|") != strings.Join(want, "|") {
		t.Fatalf("calls = %v", f.calls)
	}
	if f.summary != "Typo" || len(f.changes) != 1 {
		t.Fatalf("summary %q, changes %d", f.summary, len(f.changes))
	}
	if !strings.Contains(out, "+the") || !strings.Contains(out, "version 5") {
		t.Fatalf("out = %q", out)
	}
}

func TestEditWithoutYesIsRefusedBeforeAnyCall(t *testing.T) {
	f := newEditAPI(t)
	_, _, code := runLT(t, f.srv.URL, "work", "edit", workID, changesFile(t, oneChange), "-m", "Typo")
	if code != ExitNeedsConfirm || len(f.calls) != 0 {
		t.Fatalf("exit %d, calls %v", code, f.calls)
	}
	_, _, code = runLT(t, f.srv.URL, "work", "edit", workID, changesFile(t, oneChange), "--yes")
	if code != ExitBadArguments {
		t.Fatalf("a commit without --message: exit %d", code)
	}
}

func TestEditDryRunShowsTheDiffAndDiscards(t *testing.T) {
	f := newEditAPI(t)
	out, _, code := runLT(t, f.srv.URL, "work", "edit", workID, changesFile(t, oneChange), "--dry-run")
	if code != ExitOK || !f.discarded || !strings.Contains(out, "+the") {
		t.Fatalf("exit %d, discarded %v, out %q", code, f.discarded, out)
	}
	for _, c := range f.calls {
		if strings.HasSuffix(c, "/commit") {
			t.Fatal("a dry run committed")
		}
	}
}

func TestARefusedBatchNamesTheChangeAndLeavesNoSession(t *testing.T) {
	f := newEditAPI(t)
	f.refuse = true
	_, errOut, code := runLT(t, f.srv.URL, "work", "edit", workID, changesFile(t, oneChange), "-m", "x", "--yes")
	if code != ExitAPIError {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(errOut, "Nothing in this batch was applied") || !strings.Contains(errOut, "item 1: ambiguous: `old` occurs 3 times") {
		t.Fatalf("stderr = %q", errOut)
	}
	if !f.discarded {
		t.Fatal("the session this run opened was left open")
	}
}

func TestAStaleCommitSaysWhatMoved(t *testing.T) {
	f := newEditAPI(t)
	f.stale = true
	_, errOut, code := runLT(t, f.srv.URL, "work", "edit", workID, changesFile(t, oneChange), "-m", "x", "--yes")
	if code != ExitAPIError || !strings.Contains(errOut, "The live text changed") {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
}

func TestEditFileShapes(t *testing.T) {
	if _, err := readEditFile(changesFile(t, `{"structure":[{"op":"delete","division_id":"x"}]}`)); err != nil {
		t.Fatalf("structure only: %v", err)
	}
	if _, err := readEditFile(changesFile(t, `[]`)); err == nil {
		t.Fatal("an empty file was accepted")
	}
}
