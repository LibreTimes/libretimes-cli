package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientSendsBearerAndUserAgent(t *testing.T) {
	var gotAuth, gotUA, gotAccept string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotUA, gotAccept = r.Header.Get("Authorization"), r.Header.Get("User-Agent"), r.Header.Get("Accept")
		w.Write([]byte(`{"id":"p1","username":"kolmogorov"}`))
	}))
	defer server.Close()

	client := New(server.URL, "tok123", "v1.2.3")
	if _, err := client.Me(context.Background()); err != nil {
		t.Fatalf("Me: %v", err)
	}

	if gotAuth != "Bearer tok123" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if !strings.HasPrefix(gotUA, "lt/v1.2.3") {
		t.Errorf("User-Agent = %q, want it to carry the version", gotUA)
	}
	if gotAccept != "application/json" {
		t.Errorf("Accept = %q", gotAccept)
	}
}

func TestClientOmitsAuthorizationWhenAnonymous(t *testing.T) {
	var present bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, present = r.Header["Authorization"]
		w.Write([]byte(`{}`))
	}))
	defer server.Close()

	client := New(server.URL, "", "test")
	if client.Authenticated() {
		t.Error("Authenticated() should be false with no token")
	}
	_, _ = client.Me(context.Background())
	if present {
		t.Error("an anonymous client sent an Authorization header")
	}
}

// A redirect chased with an Authorization header attached is how a bearer
// token ends up at a host that was never meant to see it.
func TestClientNeverFollowsRedirects(t *testing.T) {
	var leakedTo string
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leakedTo = r.Header.Get("Authorization")
		w.Write([]byte(`{"id":"stolen"}`))
	}))
	defer sink.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, sink.URL+"/profiles/me", http.StatusFound)
	}))
	defer origin.Close()

	client := New(origin.URL, "secret-token", "test")
	_, err := client.Me(context.Background())

	if leakedTo != "" {
		t.Fatalf("the token was sent to the redirect target: %q", leakedTo)
	}
	if err == nil {
		t.Fatal("a 302 should surface as an error, not be followed silently")
	}
}

func TestClientMapsStatusToError(t *testing.T) {
	for _, status := range []int{401, 403, 404, 409, 422, 429, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
				w.Write([]byte(`{"detail":"upstream detail"}`))
			}))
			defer server.Close()

			_, err := New(server.URL, "t", "test").Me(context.Background())
			apiErr, ok := err.(*Error)
			if !ok {
				t.Fatalf("err = %T, want *Error", err)
			}
			if status == 404 && apiErr.Message != NotFoundText {
				t.Errorf("404 did not use the constant: %q", apiErr.Message)
			}
		})
	}
}

func TestFindPublicationByImportKeyTreats404AsAbsent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("key"); got != "content/ru/integrals" {
			t.Errorf("key = %q", got)
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	ref, err := New(server.URL, "t", "test").
		FindPublicationByImportKey(context.Background(), "content/ru/integrals")
	if err != nil {
		t.Fatalf("a 404 here means 'never imported', not an error: %v", err)
	}
	if ref != nil {
		t.Errorf("ref = %+v, want nil", ref)
	}
}

// The key is immutable and the row is addressed by id on update; sending it
// is at best ignored and at worst a 422.
func TestUpdateDropsImportKey(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		decodeJSON(t, r, &body)
		w.Write([]byte(`{"id":"pub-1"}`))
	}))
	defer server.Close()

	_, err := New(server.URL, "t", "test").UpdatePublication(context.Background(), "pub-1",
		PublicationInput{Title: "T", ImportKey: "should-not-be-sent"})
	if err != nil {
		t.Fatalf("UpdatePublication: %v", err)
	}
	if _, present := body["import_key"]; present {
		t.Errorf("update sent import_key: %v", body)
	}
}

// omitempty matters: a PATCH that sent zero values would clear fields the
// file never mentioned.
func TestUnsetFieldsAreOmitted(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		decodeJSON(t, r, &body)
		w.Write([]byte(`{"id":"pub-1"}`))
	}))
	defer server.Close()

	_, err := New(server.URL, "t", "test").CreatePublication(context.Background(),
		PublicationInput{Title: "Only a title", Type: "reference"})
	if err != nil {
		t.Fatalf("CreatePublication: %v", err)
	}

	for _, absent := range []string{"description", "license", "visibility", "ai_usage", "is_draft", "keywords", "tags", "categories"} {
		if _, present := body[absent]; present {
			t.Errorf("unset field %q was sent: %v", absent, body[absent])
		}
	}
	if body["title"] != "Only a title" {
		t.Errorf("title = %v", body["title"])
	}
	// A file's `type` is a work's kind, and everything lt creates is hosted.
	if body["kind"] != "reference" || body["origin"] != "hosted" {
		t.Errorf("kind = %v, origin = %v", body["kind"], body["origin"])
	}
}

// /works has no `is_draft` on an update: the lifecycle moves by verb, after
// the field edits, and a publish states the visibility: the API's publish
// applies one and defaults it to `public`, so a bodyless publish made a
// `visibility: private` file public (2026-10-07).
func TestUpdateMovesTheLifecycleByVerb(t *testing.T) {
	var calls []string
	var patch, published map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.Path)
		switch r.Method {
		case http.MethodPatch:
			decodeJSON(t, r, &patch)
			w.Write([]byte(`{"id":"pub-1","is_draft":true,"visibility":"private"}`))
		case http.MethodPost:
			published = nil
			if r.ContentLength > 0 {
				decodeJSON(t, r, &published)
			}
			w.Write([]byte(`{"id":"pub-1","is_draft":false}`))
		default:
			w.Write([]byte(`{"id":"pub-1","is_draft":true,"visibility":"by_link"}`))
		}
	}))
	defer server.Close()
	client := New(server.URL, "t", "test")
	publish := false

	out, err := client.UpdatePublication(context.Background(), "pub-1",
		PublicationInput{Title: "T", Visibility: "private", IsDraft: &publish})
	if err != nil {
		t.Fatalf("UpdatePublication: %v", err)
	}
	if got := strings.Join(calls, " | "); got != "PATCH /works/pub-1 | POST /works/pub-1/publish" {
		t.Errorf("calls = %q", got)
	}
	if _, present := patch["is_draft"]; present || out.IsDraft {
		t.Errorf("patch = %v, published = %v", patch, !out.IsDraft)
	}
	if published["visibility"] != "private" {
		t.Errorf("publish body = %v, want the file's visibility", published)
	}

	// Nothing but the lifecycle: a read, not an empty PATCH.
	calls = nil
	if _, err := client.UpdatePublication(context.Background(), "pub-1",
		PublicationInput{IsDraft: &publish}); err != nil {
		t.Fatalf("UpdatePublication: %v", err)
	}
	if got := strings.Join(calls, " | "); got != "GET /works/pub-1 | POST /works/pub-1/publish" {
		t.Errorf("calls = %q", got)
	}
	// The file names no visibility: the work keeps the one it has.
	if published["visibility"] != "by_link" {
		t.Errorf("publish body = %v, want the work's own visibility", published)
	}
}

// A byline row's `consent` is read as the status lt reconciles on; a
// connection's credit is on the page, so it counts as accepted.
func TestBylineConsentReadsAsStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`[{"profile_id":"a","consent":"credited"},{"profile_id":"b","consent":"declined"}]`))
	}))
	defer server.Close()

	rows, err := New(server.URL, "t", "test").ListAuthors(context.Background(), "pub-1")
	if err != nil {
		t.Fatalf("ListAuthors: %v", err)
	}
	if rows[0].Status != "accepted" || rows[1].Status != "declined" {
		t.Errorf("rows = %+v", rows)
	}
}
