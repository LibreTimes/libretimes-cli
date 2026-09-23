package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/The-LibreTimes/libretimes-cli/internal/api"
)

// recorder captures what actually went on the wire. For a reconcile tool the
// bug worth catching is "created when it should have updated", which is a
// request shape, not a response value.
type recorder struct {
	mu       []string // "METHOD /path" in order
	bodies   []map[string]any
	handler  func(w http.ResponseWriter, r *http.Request) bool
	server   *httptest.Server
	writeSum int
}

func newRecorder(t *testing.T, handler func(w http.ResponseWriter, r *http.Request) bool) *recorder {
	t.Helper()
	rec := &recorder{handler: handler}
	rec.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.mu = append(rec.mu, r.Method+" "+r.URL.Path)
		if r.Method == http.MethodPost || r.Method == http.MethodPatch {
			rec.writeSum++
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			rec.bodies = append(rec.bodies, body)
		}
		if rec.handler != nil && rec.handler(w, r) {
			return
		}
		w.Write([]byte(`{"id":"pub-new"}`))
	}))
	t.Cleanup(rec.server.Close)
	return rec
}

func (r *recorder) calls() string { return strings.Join(r.mu, " | ") }

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

const sampleDoc = `---
title: Integrals
type: reference
import_key: content/ru/integrals
---
Body.`

// Find-then-update, always. A create issued first would 409 permanently for
// exactly the rows a conflict-fallback would need to handle.
func TestPublishFindsBeforeWriting(t *testing.T) {
	rec := newRecorder(t, func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/by-import-key") {
			w.Write([]byte(`{"id":"pub-7","import_key":"content/ru/integrals","removed":false}`))
			return true
		}
		return false
	})

	path := writeFile(t, t.TempDir(), "doc.md", sampleDoc)
	client := api.New(rec.server.URL, "tok", "test")

	result := publishOne(context.Background(), client, path, false, nil)

	if result.Action != actionUpdate {
		t.Fatalf("action = %q, want %q (%s)", result.Action, actionUpdate, result.Message)
	}
	if want := "GET /publications/me/by-import-key | PATCH /publications/pub-7"; rec.calls() != want {
		t.Errorf("calls = %q, want %q", rec.calls(), want)
	}
}

func TestPublishCreatesWhenTheKeyIsUnknown(t *testing.T) {
	rec := newRecorder(t, func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/by-import-key") {
			w.WriteHeader(http.StatusNotFound)
			return true
		}
		return false
	})

	path := writeFile(t, t.TempDir(), "doc.md", sampleDoc)
	result := publishOne(context.Background(), api.New(rec.server.URL, "tok", "test"), path, false, nil)

	if result.Action != actionCreate {
		t.Fatalf("action = %q, want %q (%s)", result.Action, actionCreate, result.Message)
	}
	if want := "GET /publications/me/by-import-key | POST /publications"; rec.calls() != want {
		t.Errorf("calls = %q, want %q", rec.calls(), want)
	}
	if got := rec.bodies[0]["import_key"]; got != "content/ru/integrals" {
		t.Errorf("create did not carry the import_key: %v", got)
	}
}

// Finding a removed row is not permission to restore it. A re-run of an import
// is not consent to republish work its author withdrew.
func TestPublishRefusesToResurrectARemovedRow(t *testing.T) {
	rec := newRecorder(t, func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/by-import-key") {
			w.Write([]byte(`{"id":"pub-9","import_key":"content/ru/integrals","removed":true,"title":"Integrals"}`))
			return true
		}
		return false
	})

	path := writeFile(t, t.TempDir(), "doc.md", sampleDoc)
	result := publishOne(context.Background(), api.New(rec.server.URL, "tok", "test"), path, false, nil)

	if result.Action != actionBlocked {
		t.Fatalf("action = %q, want %q", result.Action, actionBlocked)
	}
	if rec.writeSum != 0 {
		t.Errorf("a blocked file still issued %d write(s): %s", rec.writeSum, rec.calls())
	}
	if !strings.Contains(result.Message, "pub-9") {
		t.Errorf("message should name the publication holding the key: %q", result.Message)
	}
}

// A dry run's whole value is that it promises exactly what the real run does,
// and writes nothing while doing it.
func TestDryRunIssuesNoWrites(t *testing.T) {
	rec := newRecorder(t, func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/by-import-key") {
			w.Write([]byte(`{"id":"pub-7","removed":false}`))
			return true
		}
		return false
	})

	path := writeFile(t, t.TempDir(), "doc.md", sampleDoc)
	result := publishOne(context.Background(), api.New(rec.server.URL, "tok", "test"), path, true, nil)

	if result.Action != actionUpdate {
		t.Errorf("action = %q, want %q", result.Action, actionUpdate)
	}
	if rec.writeSum != 0 {
		t.Errorf("--dry-run issued %d write(s): %s", rec.writeSum, rec.calls())
	}
}

// A dry run must report `blocked` rather than promising an update the real run
// will refuse.
func TestDryRunReportsBlockedNotUpdate(t *testing.T) {
	rec := newRecorder(t, func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/by-import-key") {
			w.Write([]byte(`{"id":"pub-9","removed":true}`))
			return true
		}
		return false
	})

	path := writeFile(t, t.TempDir(), "doc.md", sampleDoc)
	result := publishOne(context.Background(), api.New(rec.server.URL, "tok", "test"), path, true, nil)

	if result.Action != actionBlocked {
		t.Errorf("dry-run action = %q, want %q", result.Action, actionBlocked)
	}
}

// type is required by both publications-service and the BFF. Catching it here
// names the file and the fix instead of relaying a 422.
func TestMissingTypeFailsBeforeAnyRequest(t *testing.T) {
	rec := newRecorder(t, nil)
	path := writeFile(t, t.TempDir(), "doc.md", "---\ntitle: T\nimport_key: k\n---\nbody")

	result := publishOne(context.Background(), api.New(rec.server.URL, "tok", "test"), path, false, nil)

	if result.Action != actionFailed {
		t.Fatalf("action = %q, want %q", result.Action, actionFailed)
	}
	if len(rec.mu) != 0 {
		t.Errorf("a file with no type still hit the API: %s", rec.calls())
	}
	if !strings.Contains(result.Message, "type") {
		t.Errorf("message should name the missing field: %q", result.Message)
	}
}

// The Python default was lt:<stem>, which collides the moment a ru/ and an en/
// tree share a slug — and a collision silently overwrites one language with
// the other.
func TestDefaultImportKeyDistinguishesSiblingTrees(t *testing.T) {
	ru := defaultImportKey("content/ru/integrals.md")
	en := defaultImportKey("content/en/integrals.md")

	if ru == en {
		t.Fatalf("ru and en derived the same key: %q", ru)
	}
	if !strings.HasPrefix(ru, "lt:") {
		t.Errorf("key = %q, want an lt: prefix", ru)
	}
	if !strings.Contains(ru, "ru") || !strings.Contains(en, "en") {
		t.Errorf("keys should carry the parent directory: %q / %q", ru, en)
	}
}

func TestExplicitImportKeyWins(t *testing.T) {
	rec := newRecorder(t, func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/by-import-key") {
			if got := r.URL.Query().Get("key"); got != "content/ru/integrals" {
				t.Errorf("looked up %q, want the explicit key", got)
			}
			w.WriteHeader(http.StatusNotFound)
			return true
		}
		return false
	})

	path := writeFile(t, t.TempDir(), "unrelated-filename.md", sampleDoc)
	publishOne(context.Background(), api.New(rec.server.URL, "tok", "test"), path, false, nil)
}

// A 404 from anywhere must not become an existence oracle in a per-file line.
func TestFailureMessageUsesTheConstantFor404(t *testing.T) {
	rec := newRecorder(t, func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/by-import-key") {
			w.Write([]byte(`{"id":"pub-7","removed":false}`))
			return true
		}
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"detail":"publication pub-7 belongs to @someone-else"}`))
		return true
	})

	path := writeFile(t, t.TempDir(), "doc.md", sampleDoc)
	result := publishOne(context.Background(), api.New(rec.server.URL, "tok", "test"), path, false, nil)

	if result.Action != actionFailed {
		t.Fatalf("action = %q", result.Action)
	}
	if strings.Contains(result.Message, "someone-else") {
		t.Errorf("the per-file message leaked the upstream 404 detail: %q", result.Message)
	}
	if !strings.Contains(result.Message, api.NotFoundText) {
		t.Errorf("message = %q, want the 404 constant", result.Message)
	}
}
