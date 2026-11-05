package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// The BFF relays works-service's body untouched, so the refusal arrives in the
// platform envelope; a domain service answering directly uses the bare one.
// Both are read.
func TestRefusalIsReadFromEitherEnvelope(t *testing.T) {
	bodies := map[string]string{
		"platform": `{"error":{"status_code":409,"type":"conflict","detail":{"error_code":"VENUE_REQUIRED",` +
			`"message":"A book is published by a venue, so it stays a draft until one does.","kind":"book"}}}`,
		"bare": `{"detail":{"error_code":"VENUE_REQUIRED","message":"...","kind":"book"}}`,
	}
	for name, body := range bodies {
		err := responseError(409, []byte(body), "post works")
		var refusal *Refusal
		if !errors.As(err, &refusal) {
			t.Fatalf("%s: not a *Refusal: %#v", name, err)
		}
		var apiErr *Error
		if !errors.As(err, &apiErr) || apiErr.Code != CodeVenueRequired {
			t.Fatalf("%s: code = %v, want %s", name, apiErr, CodeVenueRequired)
		}
		if refusal.Kind != "book" {
			t.Errorf("%s: kind = %q", name, refusal.Kind)
		}
		for _, want := range []string{"venue", "`book`", "Nothing was published", "`is_draft: true`"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: message lacks %q: %s", name, want, err.Error())
			}
		}
	}
}

func TestWorkIncompleteNamesTheMissingFieldsInWords(t *testing.T) {
	body := `{"error":{"status_code":409,"detail":{"error_code":"WORK_INCOMPLETE",` +
		`"message":"This work is missing what it needs to publish: abstract, byline.",` +
		`"missing":["abstract","byline","some_future_key"]}}}`
	err := responseError(409, []byte(body), "post works")

	var refusal *Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("not a *Refusal: %#v", err)
	}
	if !slices.Equal(refusal.Missing, []string{"abstract", "byline", "some_future_key"}) {
		t.Errorf("missing = %v", refusal.Missing)
	}
	if refusal.Err.Code != CodeWorkIncomplete {
		t.Errorf("code = %q", refusal.Err.Code)
	}
	msg := err.Error()
	for _, want := range []string{"an abstract (`description`)", "an author on the byline",
		"`some_future_key`", "Nothing was published"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message lacks %q: %s", want, msg)
		}
	}
}

func TestFamilyChangeSaysBothKinds(t *testing.T) {
	body := `{"error":{"detail":{"error_code":"FAMILY_CHANGE","kind":"lecture","requested_kind":"report"}}}`
	err := responseError(409, []byte(body), "patch works")
	var refusal *Refusal
	if !errors.As(err, &refusal) || refusal.Err.Code != CodeFamilyChange {
		t.Fatalf("not a family_change refusal: %#v", err)
	}
	if !strings.Contains(err.Error(), "`lecture`") || !strings.Contains(err.Error(), "`report`") {
		t.Errorf("message does not name both kinds: %s", err.Error())
	}
}

// A 409 that is not a registry refusal is the ordinary conflict, unchanged.
func TestOtherConflictsAreUnchanged(t *testing.T) {
	for _, body := range []string{
		`{"detail":"import_key 'x' already used by another work of yours"}`,
		`{"error":{"detail":{"error_code":"SOMETHING_ELSE","message":"no"}}}`,
		`not json`,
	} {
		err := responseError(409, []byte(body), "post works")
		var refusal *Refusal
		if errors.As(err, &refusal) {
			t.Errorf("%s: read as a refusal", body)
		}
		var apiErr *Error
		if !errors.As(err, &apiErr) || apiErr.Code != CodeConflict {
			t.Errorf("%s: code = %v, want conflict", body, err)
		}
	}
}

// The 404 rule holds through responseError: a body shaped like a refusal does
// not change a 404's text.
func TestRefusalShapeNeverLeaksThroughA404(t *testing.T) {
	body := `{"error":{"detail":{"error_code":"VENUE_REQUIRED","kind":"book"}}}`
	err := responseError(404, []byte(body), "post works")
	if err.Error() != NotFoundText {
		t.Errorf("404 text = %q", err.Error())
	}
}

// The registry is read once per client, however many files ask.
func TestWorkTypesIsReadOncePerClient(t *testing.T) {
	reads := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/work-types" {
			t.Errorf("unexpected %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "" {
			t.Errorf("the registry is public; no token is needed to read it")
		}
		reads++
		w.Write([]byte(`{"version":1,"kinds":[` +
			`{"kind":"lecture","family":"page","publish":"self","required":["title","language","body"]},` +
			`{"kind":"book","family":"page","publish":"venue","required":["title"]}],` +
			`"external_types":[],"external_fallback":"other","a_field_from_the_future":true}`))
	}))
	defer srv.Close()

	client := New(srv.URL, "", "test")
	for range 3 {
		types, err := client.WorkTypes(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if got := types.KindNames(); !slices.Equal(got, []string{"lecture", "book"}) {
			t.Fatalf("kinds = %v", got)
		}
		if types.Kind("book").Publish != PublishVenue || types.Kind("essay") != nil {
			t.Fatalf("Kind lookup is wrong")
		}
	}
	if reads != 1 {
		t.Errorf("registry read %d times, want 1", reads)
	}
}

// A failed read is not cached: the next caller asks again.
func TestWorkTypesFailureIsNotCached(t *testing.T) {
	reads := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads++
		if reads == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Write([]byte(`{"version":1,"kinds":[{"kind":"lecture"}],"external_types":[]}`))
	}))
	defer srv.Close()

	client := New(srv.URL, "", "test")
	if _, err := client.WorkTypes(context.Background()); err == nil {
		t.Fatal("the first read should fail")
	}
	if _, err := client.WorkTypes(context.Background()); err != nil {
		t.Fatalf("the second read should succeed: %v", err)
	}
}
