package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/The-LibreTimes/libretimes-cli/internal/api"
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

const termsDoc = `---
title: Книги по алгебре
type: notes
language: ru
import_key: lt:tests/terms
tags: [линейная алгебра, книги]
categories: [algebra]
category_ids: [019f60ef-0000-7000-8000-000000000000]
---
Body.`

// A file's tags and categories go to the server as written (it resolves
// them), category_ids still go but warn, and the save's report comes back on
// the result.
func TestPublishSendsTermsAsWrittenAndKeepsTheReport(t *testing.T) {
	rec := newRecorder(t, func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/by-import-key") {
			w.WriteHeader(http.StatusNotFound)
			return true
		}
		if r.Method == http.MethodPost {
			w.Write([]byte(`{"id":"w1","terms":[
				{"term":"algebra","field":"categories","became":"category","category":{"id":"c","key":"algebra","name":"Алгебра","context":"Математика","match":"key"}},
				{"term":"линейная алгебра","field":"tags","became":"category","code":"TAG_IS_CATEGORY","category":` + hitJSON + `},
				{"term":"книги","field":"tags","became":"keyword","code":"GENERIC_TAG"}]}`))
			return true
		}
		return false
	})
	path := writeFile(t, t.TempDir(), "doc.md", termsDoc)
	result := publishOne(context.Background(), api.New(rec.server.URL, "tok", "test"), path, false, nil)
	if result.Action != actionCreate {
		t.Fatalf("action = %q (%s)", result.Action, result.Message)
	}
	body := rec.bodies[0]
	if tags, _ := body["tags"].([]any); len(tags) != 2 || tags[0] != "линейная алгебра" {
		t.Errorf("tags = %v", body["tags"])
	}
	if cats, _ := body["categories"].([]any); len(cats) != 1 || cats[0] != "algebra" {
		t.Errorf("categories = %v", body["categories"])
	}
	if _, folded := body["keywords"]; folded {
		t.Errorf("tags were folded into keywords: %v", body["keywords"])
	}
	if len(result.Terms) != 3 || result.Terms[1].Code != "TAG_IS_CATEGORY" {
		t.Errorf("terms = %+v", result.Terms)
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "deprecated") {
		t.Errorf("warnings = %v", result.Warnings)
	}
	var out, errOut bytes.Buffer
	renderResult(&env{out: &out, err: &errOut}, result)
	for _, want := range []string{`"книги" -> keyword [GENERIC_TAG`, `-> category linear-algebra (Linear algebra — Algebra · Mathematics)`} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
	if !strings.Contains(errOut.String(), "category_ids` is deprecated") {
		t.Errorf("no deprecation warning on stderr: %q", errOut.String())
	}
}

// An unknown key is a 422 that names what it might have meant.
func TestAnUnknownKeyFailsWithItsSuggestions(t *testing.T) {
	rec := newRecorder(t, func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/by-import-key") {
			w.WriteHeader(http.StatusNotFound)
			return true
		}
		w.WriteHeader(http.StatusUnprocessableEntity)
		w.Write([]byte(`{"error":{"status_code":422,"detail":{"error_code":"CATEGORY_NOT_FOUND",
			"message":"Unknown category key: algebre","terms":[{"term":"algebre","field":"categories",
			"became":"dropped","code":"CATEGORY_NOT_FOUND","suggestions":[` + hitJSON + `]}]}}}`))
		return true
	})
	path := writeFile(t, t.TempDir(), "doc.md", strings.Replace(termsDoc, "[algebra]", "[algebre]", 1))
	result := publishOne(context.Background(), api.New(rec.server.URL, "tok", "test"), path, false, nil)
	if result.Action != actionFailed || result.Code != "validation_error" {
		t.Fatalf("result = %+v", result)
	}
	if !strings.Contains(result.Message, `Unknown category key: algebre`) ||
		!strings.Contains(result.Message, `"algebre": did you mean linear-algebra?`) {
		t.Errorf("message = %q", result.Message)
	}
}
