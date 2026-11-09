package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The taxonomy reads are public: `lt categories` must work with no sign-in
// and must not send a credential it does not have (A21: a pure client of the
// public API). These run against a stub BFF.
func runCategories(t *testing.T, handler http.HandlerFunc, args ...string) (string, error, []*http.Request) {
	t.Helper()
	var seen []*http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := new(bytes.Buffer)
		body.ReadFrom(r.Body)
		r.Body = nopCloser{bytes.NewReader(body.Bytes())}
		clone := r.Clone(r.Context())
		clone.Body = nopCloser{bytes.NewReader(body.Bytes())}
		seen = append(seen, clone)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LIBRETIMES_TOKEN", "")

	root, e := NewRootCommand()
	var out, errOut bytes.Buffer
	e.out, e.err = &out, &errOut
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(append([]string{"--api-url", srv.URL}, args...))
	err := root.Execute()
	return out.String(), err, seen
}

type nopCloser struct{ *bytes.Reader }

func (nopCloser) Close() error { return nil }

const hitJSON = `{"id":"c1","key":"linear-algebra","level":"field","name":"Linear algebra","language":"en",
"context":"Algebra · Mathematics","context_keys":["algebra","mathematics"],"match":"alias","matched_text":"linalg"}`

func TestCategoriesSearchIsAnonymousAndShowsContext(t *testing.T) {
	out, err, seen := runCategories(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("[" + hitJSON + "]"))
	}, "categories", "search", "lin", "alg", "--lang", "en")
	if err != nil {
		t.Fatal(err)
	}
	r := seen[0]
	if r.URL.Path != "/categories/suggest" || r.URL.Query().Get("q") != "lin alg" || r.URL.Query().Get("lang") != "en" {
		t.Errorf("request = %s %s", r.URL.Path, r.URL.RawQuery)
	}
	if h := r.Header.Get("Authorization"); h != "" {
		t.Errorf("sent a credential it does not have: %q", h)
	}
	if !strings.Contains(out, "Linear algebra — Algebra · Mathematics") || !strings.Contains(out, "alias: linalg") {
		t.Errorf("output = %q", out)
	}
}

func TestCategoriesResolveSendsTermsAndKeys(t *testing.T) {
	out, err, seen := runCategories(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"terms":[
			{"term":"linalg","text":"linalg","norm":"linalg","category":` + hitJSON + `,"generic":false,"suggestions":[]},
			{"term":"книги","text":"книги","norm":"книги","category":null,"generic":true,"suggestions":[]},
			{"term":"foundations","text":"foundations","norm":"foundations","category":null,"generic":false,"suggestions":[` + hitJSON + `]}],
		  "keys":[{"key":"algebre","category":null,"suggestions":[` + hitJSON + `]}],"ids":[]}`))
	}, "categories", "resolve", "linalg", "книги", "foundations", "--key", "algebre", "--lang", "ru")
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	json.NewDecoder(seen[0].Body).Decode(&body)
	if seen[0].Method != http.MethodPost || seen[0].URL.Path != "/categories/resolve-terms" {
		t.Fatalf("request = %s %s", seen[0].Method, seen[0].URL.Path)
	}
	if body["lang"] != "ru" || len(body["terms"].([]any)) != 3 || body["keys"].([]any)[0] != "algebre" {
		t.Errorf("body = %v", body)
	}
	for _, want := range []string{
		`"linalg" -> category linear-algebra`,
		`"книги" -> keyword "книги"  [GENERIC_TAG`,
		`"foundations" -> keyword "foundations"`,
		"did you mean:",
		"key algebre -> no such category",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

func TestCategoriesResolveNeedsSomething(t *testing.T) {
	_, err, _ := runCategories(t, func(w http.ResponseWriter, r *http.Request) {}, "categories", "resolve")
	if err == nil || !strings.Contains(err.Error(), "at least one term") {
		t.Fatalf("err = %v", err)
	}
}
