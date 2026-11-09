package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/The-LibreTimes/libretimes-cli/internal/api"
)

const annotatedText = "Intro: the word holds.\n\nA typo: teh word, then the word again."

// annotationAPI fakes one work's text and its annotation routes.
type annotationAPI struct {
	mu      sync.Mutex
	calls   []string
	created map[string]json.RawMessage
	decided map[string]any
	query   string
	srv     *httptest.Server
}

func newAnnotationAPI(t *testing.T) *annotationAPI {
	f := &annotationAPI{}
	f.srv = httptest.NewServer(http.HandlerFunc(f.handle))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *annotationAPI) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, r.Method+" "+r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	work := "/works/" + workID
	switch {
	case r.Method == "GET" && r.URL.Path == work:
		_, _ = w.Write([]byte(`{"kind":"notes","language":"en","content":` + mustJSON(annotatedText) +
			`,"current_version":3,"content_language":{"served":"en"}}`))
	case r.Method == "POST" && r.URL.Path == work+"/annotations":
		_ = json.NewDecoder(r.Body).Decode(&f.created)
		_, _ = w.Write([]byte(`{"id":"s1","kind":"suggestion","visibility":"public","status":"open"}`))
	case r.Method == "GET" && r.URL.Path == work+"/annotations":
		f.query = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"total":2,"items":[` +
			`{"id":"s1","kind":"suggestion","visibility":"public","status":"open","author":{"username":"ada"},` +
			`"anchor":{"target":{"field":"content"},"quote":{"exact":"teh word"}},"change":{"old":"teh word","new":"the word"},"body":"Typo."},` +
			`{"id":"gone","kind":"comment","visibility":"public","is_deleted":true,"replies_count":0}]}`))
	case r.Method == "POST" && r.URL.Path == work+"/annotations/resolve":
		_ = json.NewDecoder(r.Body).Decode(&f.decided)
		_, _ = w.Write([]byte(`{"results":[{"id":"s1","ok":true,"status":"accepted"}],"edit_session_id":"e1","version_number":4}`))
	default:
		w.WriteHeader(404)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestSuggestAnchorsTheQuotedMarkdownAndSendsTheChange(t *testing.T) {
	f := newAnnotationAPI(t)
	out, errOut, code := runLT(t, f.srv.URL, "work", "suggest", workID, "--old", "teh word", "--new", "the word",
		"--note", "Typo.", "--yes")
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	var anchor api.Anchor
	_ = json.Unmarshal(f.created["anchor"], &anchor)
	if anchor.Quote.Exact != "teh word" || anchor.Target.Language != "en" || anchor.Target.Field != "content" {
		t.Fatalf("anchor = %+v", anchor)
	}
	at := strings.Index(annotatedText, "teh word")
	if anchor.Hint == nil || anchor.Hint.Start != at || anchor.Hint.End != at+len("teh word") {
		t.Fatalf("hint = %+v", anchor.Hint)
	}
	if anchor.Version == nil || *anchor.Version != 3 || !strings.HasSuffix(anchor.Quote.Prefix, "A typo: ") {
		t.Fatalf("version/prefix = %v %q", anchor.Version, anchor.Quote.Prefix)
	}
	if string(f.created["change"]) != `{"old":"teh word","new":"the word"}` || string(f.created["kind"]) != `"suggestion"` {
		t.Fatalf("created = %s", f.created)
	}
	if !strings.Contains(out, "suggested s1") {
		t.Fatalf("out = %q", out)
	}
}

func TestSuggestRefusesARepeatedOrMissingPassageBeforeWriting(t *testing.T) {
	for old, why := range map[string]string{"the word": "occurs 2 times", "nowhere": "does not occur"} {
		f := newAnnotationAPI(t)
		_, errOut, code := runLT(t, f.srv.URL, "work", "suggest", workID, "--old", old, "--new", "x", "--yes")
		if code != ExitBadArguments || !strings.Contains(errOut, why) {
			t.Fatalf("%q: exit %d, %q", old, code, errOut)
		}
		for _, c := range f.calls {
			if strings.HasPrefix(c, "POST") {
				t.Fatalf("%q: wrote %s", old, c)
			}
		}
	}
}

func TestSuggestWithoutYesIsRefusedBeforeAnyCall(t *testing.T) {
	f := newAnnotationAPI(t)
	_, _, code := runLT(t, f.srv.URL, "work", "suggest", workID, "--old", "teh", "--new", "the")
	if code != ExitNeedsConfirm || len(f.calls) != 0 {
		t.Fatalf("exit %d, calls %v", code, f.calls)
	}
}

func TestReviewListsOpenSuggestionsWithTheirChange(t *testing.T) {
	f := newAnnotationAPI(t)
	out, _, code := runLT(t, f.srv.URL, "work", "review", workID)
	if code != ExitOK {
		t.Fatalf("exit %d", code)
	}
	if f.query != "kind=suggestion&kind=erratum&limit=100&status=open" {
		t.Fatalf("query = %q", f.query)
	}
	for _, want := range []string{"s1  suggestion · open  @ada", "- teh word", "+ the word", "Typo."} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in %q", want, out)
		}
	}
	if strings.Contains(out, "gone") {
		t.Fatal("a deleted annotation with no replies was listed")
	}
}

func TestAcceptNamesLtAndReportsTheVersion(t *testing.T) {
	f := newAnnotationAPI(t)
	out, errOut, code := runLT(t, f.srv.URL, "work", "accept", workID, "s1", "-m", "Fix typo", "--yes")
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if f.decided["action"] != "accept" || f.decided["client"] != "lt" || f.decided["summary"] != "Fix typo" {
		t.Fatalf("decided = %v", f.decided)
	}
	if !strings.Contains(out, "s1  accepted") || !strings.Contains(out, "version 4") {
		t.Fatalf("out = %q", out)
	}
}

func TestAnchorOffsetsAreUTF16(t *testing.T) {
	text := "𝔄 opens it; the passage is here."
	a, err := api.AnchorFor(text, "the passage", api.AnchorTarget{Field: "content"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	// 𝔄 is four bytes, one rune and two UTF-16 units.
	if want := len([]rune(text[:strings.Index(text, "the passage")])) + 1; a.Hint.Start != want {
		t.Fatalf("start = %d, want %d", a.Hint.Start, want)
	}
	if a.Quote.Prefix != "𝔄 opens it; " {
		t.Fatalf("prefix = %q", a.Quote.Prefix)
	}
}
