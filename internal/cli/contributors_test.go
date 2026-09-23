package cli

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/The-LibreTimes/libretimes-cli/internal/api"
)

const (
	selfID     = "019f0000-0000-7000-8000-000000000001"
	lecturerID = "019f0000-0000-7000-8000-000000000002"
	editorID   = "019f0000-0000-7000-8000-000000000003"
)

const lectureDoc = `---
title: Lecture 1
type: lecture
import_key: courses/ivanov/analysis/01
contributors:
  - profile: "@ivanov-ii-a1b2c3"
    role: lecturer
---
Body.`

// profilesHandler answers the two profile reads every contributor test needs,
// and reports whether it did.
func profilesHandler(w http.ResponseWriter, r *http.Request) bool {
	switch r.URL.Path {
	case "/profiles/me":
		w.Write([]byte(`{"id":"` + selfID + `","username":"notetaker"}`))
		return true
	case "/profiles/ivanov-ii-a1b2c3":
		w.Write([]byte(`{"id":"` + lecturerID + `","username":"ivanov-ii-a1b2c3"}`))
		return true
	case "/profiles/petrov":
		w.Write([]byte(`{"id":"` + editorID + `","username":"petrov"}`))
		return true
	}
	return false
}

// The lecturer credit rides on the create itself: resolved to a profile_id,
// always `credited`, never an access level the file could ask for.
func TestCreateCarriesTheResolvedCredit(t *testing.T) {
	rec := newRecorder(t, func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/by-import-key") {
			w.WriteHeader(http.StatusNotFound)
			return true
		}
		return profilesHandler(w, r)
	})

	path := writeFile(t, t.TempDir(), "01.md", lectureDoc)
	result := publishOne(context.Background(), api.New(rec.server.URL, "tok", "test"), path, false, nil)

	if result.Action != actionCreate {
		t.Fatalf("action = %q, want %q (%s)", result.Action, actionCreate, result.Message)
	}
	want := "GET /profiles/me | GET /profiles/ivanov-ii-a1b2c3 | " +
		"GET /publications/me/by-import-key | POST /publications"
	if rec.calls() != want {
		t.Errorf("calls = %q, want %q", rec.calls(), want)
	}
	authors, _ := rec.bodies[0]["authors"].([]any)
	if len(authors) != 1 {
		t.Fatalf("authors = %v, want one entry", rec.bodies[0]["authors"])
	}
	got := authors[0].(map[string]any)
	if got["profile_id"] != lecturerID || got["role"] != "lecturer" || got["access"] != "credited" {
		t.Errorf("author = %v", got)
	}
	if _, ok := rec.bodies[0]["creator_role"]; ok {
		t.Errorf("creator_role sent although the file does not list the signed-in account")
	}
	if len(result.Contributors) != 1 || result.Contributors[0].Action != creditAdd {
		t.Errorf("contributors = %+v", result.Contributors)
	}
}

// Inviting yourself is a 400 downstream. The signed-in account's entry
// becomes creator_role instead.
func TestCreateTurnsTheSignedInAccountIntoCreatorRole(t *testing.T) {
	rec := newRecorder(t, func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/by-import-key") {
			w.WriteHeader(http.StatusNotFound)
			return true
		}
		return profilesHandler(w, r)
	})

	doc := strings.Replace(lectureDoc, "contributors:\n",
		"contributors:\n  - profile: "+selfID+"\n    role: translator\n", 1)
	path := writeFile(t, t.TempDir(), "01.md", doc)
	result := publishOne(context.Background(), api.New(rec.server.URL, "tok", "test"), path, false, nil)

	if result.Action != actionCreate {
		t.Fatalf("action = %q (%s)", result.Action, result.Message)
	}
	body := rec.bodies[0]
	if body["creator_role"] != "translator" {
		t.Errorf("creator_role = %v, want translator", body["creator_role"])
	}
	for _, a := range body["authors"].([]any) {
		if a.(map[string]any)["profile_id"] == selfID {
			t.Errorf("the signed-in account was invited to its own publication")
		}
	}
}

// On a re-run the update body carries no byline, so the byline is diffed. A
// declined row counts as present: a re-publish must not ask again.
func TestUpdateInvitesOnlyTheMissingCredits(t *testing.T) {
	doc := strings.Replace(lectureDoc, "    role: lecturer\n",
		"    role: lecturer\n  - profile: petrov\n    role: editor\n", 1)
	rec := newRecorder(t, func(w http.ResponseWriter, r *http.Request) bool {
		switch {
		case strings.HasSuffix(r.URL.Path, "/by-import-key"):
			w.Write([]byte(`{"id":"pub-7","removed":false}`))
			return true
		case r.Method == http.MethodGet && r.URL.Path == "/publications/pub-7/authors":
			w.Write([]byte(`[
				{"profile_id":"` + selfID + `","role":"author","status":"accepted"},
				{"profile_id":"` + lecturerID + `","role":"lecturer","status":"declined"}
			]`))
			return true
		}
		return profilesHandler(w, r)
	})

	path := writeFile(t, t.TempDir(), "01.md", doc)
	result := publishOne(context.Background(), api.New(rec.server.URL, "tok", "test"), path, false, nil)

	if result.Action != actionUpdate {
		t.Fatalf("action = %q (%s)", result.Action, result.Message)
	}
	want := "GET /profiles/me | GET /profiles/ivanov-ii-a1b2c3 | GET /profiles/petrov | " +
		"GET /publications/me/by-import-key | GET /publications/pub-7/authors | " +
		"PATCH /publications/pub-7 | POST /publications/pub-7/authors"
	if rec.calls() != want {
		t.Errorf("calls = %q\nwant    %q", rec.calls(), want)
	}
	if _, ok := rec.bodies[0]["authors"]; ok {
		t.Errorf("the PATCH body carried authors: %v", rec.bodies[0])
	}
	if invite := rec.bodies[1]; invite["profile_id"] != editorID || invite["role"] != "editor" {
		t.Errorf("invite = %v, want petrov as editor", invite)
	}
	actions := []string{result.Contributors[0].Action, result.Contributors[1].Action}
	if actions[0] != creditPresent || actions[1] != creditAdd {
		t.Errorf("actions = %v, want [present add]", actions)
	}
}

// --dry-run reads the byline, so it reports the same diff, and writes nothing.
// A role that differs is surfaced, never rewritten.
func TestDryRunReportsTheBylineDiffWithoutWriting(t *testing.T) {
	rec := newRecorder(t, func(w http.ResponseWriter, r *http.Request) bool {
		switch {
		case strings.HasSuffix(r.URL.Path, "/by-import-key"):
			w.Write([]byte(`{"id":"pub-7","removed":false}`))
			return true
		case r.URL.Path == "/publications/pub-7/authors":
			w.Write([]byte(`[{"profile_id":"` + lecturerID + `","role":"author","status":"accepted"}]`))
			return true
		}
		return profilesHandler(w, r)
	})

	path := writeFile(t, t.TempDir(), "01.md", lectureDoc)
	result := publishOne(context.Background(), api.New(rec.server.URL, "tok", "test"), path, true, nil)

	if rec.writeSum != 0 {
		t.Errorf("--dry-run issued %d write(s): %s", rec.writeSum, rec.calls())
	}
	if len(result.Contributors) != 1 {
		t.Fatalf("contributors = %+v", result.Contributors)
	}
	c := result.Contributors[0]
	if c.Action != creditPresent || c.CurrentRole != "author" {
		t.Errorf("contributor = %+v, want present with current_role author", c)
	}
}

// A file with no contributors behaves exactly as before: no profile read, no
// byline read.
func TestNoContributorsCostsNoExtraRequest(t *testing.T) {
	rec := newRecorder(t, func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/by-import-key") {
			w.Write([]byte(`{"id":"pub-7","removed":false}`))
			return true
		}
		return false
	})

	path := writeFile(t, t.TempDir(), "doc.md", sampleDoc)
	publishOne(context.Background(), api.New(rec.server.URL, "tok", "test"), path, false, nil)

	if want := "GET /publications/me/by-import-key | PATCH /publications/pub-7"; rec.calls() != want {
		t.Errorf("calls = %q, want %q", rec.calls(), want)
	}
}

// Everything checkable locally fails before a request.
func TestAnUnknownRoleFailsBeforeAnyRequest(t *testing.T) {
	rec := newRecorder(t, nil)
	doc := strings.Replace(lectureDoc, "role: lecturer", "role: speaker", 1)
	path := writeFile(t, t.TempDir(), "01.md", doc)

	result := publishOne(context.Background(), api.New(rec.server.URL, "tok", "test"), path, false, nil)

	if result.Action != actionFailed {
		t.Fatalf("action = %q", result.Action)
	}
	if len(rec.mu) != 0 {
		t.Errorf("an invalid role still hit the API: %s", rec.calls())
	}
	if !strings.Contains(result.Message, "speaker") || !strings.Contains(result.Message, "lecturer") {
		t.Errorf("message should name the bad role and the allowed ones: %q", result.Message)
	}
}

// A handle that does not resolve stops the file before anything is written,
// and the message says which contributor while keeping the 404 constant
// byte-identical.
func TestAnUnresolvedHandleWritesNothing(t *testing.T) {
	rec := newRecorder(t, func(w http.ResponseWriter, r *http.Request) bool {
		if profilesHandler(w, r) {
			return true
		}
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"detail":"profile ghost is private"}`))
		return true
	})

	doc := strings.Replace(lectureDoc, "@ivanov-ii-a1b2c3", "@ghost", 1)
	path := writeFile(t, t.TempDir(), "01.md", doc)
	result := publishOne(context.Background(), api.New(rec.server.URL, "tok", "test"), path, false, nil)

	if result.Action != actionFailed || result.Code != api.CodeNotFound {
		t.Fatalf("action = %q, code = %q", result.Action, result.Code)
	}
	if strings.Contains(rec.calls(), "by-import-key") || rec.writeSum != 0 {
		t.Errorf("went on after an unresolved contributor: %s", rec.calls())
	}
	if !strings.Contains(result.Message, "@ghost") || !strings.Contains(result.Message, api.NotFoundText) {
		t.Errorf("message = %q, want the contributor and the 404 constant", result.Message)
	}
	if strings.Contains(result.Message, "private") {
		t.Errorf("the upstream 404 detail leaked: %q", result.Message)
	}
}

// The platform owns the handle fold; lt sends the handle as written.
func TestHandlesAreNotFoldedLocally(t *testing.T) {
	var asked string
	rec := newRecorder(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/profiles/me" {
			return profilesHandler(w, r)
		}
		if strings.HasPrefix(r.URL.Path, "/profiles/") {
			asked = r.URL.Path
			w.Write([]byte(`{"id":"` + editorID + `"}`))
			return true
		}
		w.WriteHeader(http.StatusNotFound)
		return true
	})

	doc := strings.Replace(lectureDoc, "@ivanov-ii-a1b2c3", "@Petrov", 1)
	path := writeFile(t, t.TempDir(), "01.md", doc)
	publishOne(context.Background(), api.New(rec.server.URL, "tok", "test"), path, true, nil)

	if asked != "/profiles/Petrov" {
		t.Errorf("looked up %q, want /profiles/Petrov", asked)
	}
}

// One lecturer across a course's files is one lookup.
func TestHandleLookupsAreCachedAcrossFiles(t *testing.T) {
	rec := newRecorder(t, func(w http.ResponseWriter, r *http.Request) bool {
		if strings.HasSuffix(r.URL.Path, "/by-import-key") {
			w.WriteHeader(http.StatusNotFound)
			return true
		}
		return profilesHandler(w, r)
	})

	client := api.New(rec.server.URL, "tok", "test")
	profiles := newProfileResolver(client)
	dir := t.TempDir()
	for _, name := range []string{"01.md", "02.md"} {
		doc := strings.Replace(lectureDoc, "/01", "/"+strings.TrimSuffix(name, ".md"), 1)
		publishOne(context.Background(), client, writeFile(t, dir, name, doc), true, profiles)
	}

	if n := strings.Count(rec.calls(), "GET /profiles/"); n != 2 {
		t.Errorf("%d profile reads, want 2 (me + the lecturer, once each): %s", n, rec.calls())
	}
}

func TestTheSameProfileListedTwiceIsRefused(t *testing.T) {
	rec := newRecorder(t, profilesHandler)
	doc := strings.Replace(lectureDoc, "    role: lecturer\n",
		"    role: lecturer\n  - profile: "+lecturerID+"\n    role: editor\n", 1)
	path := writeFile(t, t.TempDir(), "01.md", doc)

	result := publishOne(context.Background(), api.New(rec.server.URL, "tok", "test"), path, false, nil)

	if result.Action != actionFailed || !strings.Contains(result.Message, "same profile") {
		t.Errorf("action = %q, message = %q", result.Action, result.Message)
	}
	if rec.writeSum != 0 {
		t.Errorf("a duplicate contributor still wrote: %s", rec.calls())
	}
}

// A handle beginning with @ is always a handle, even one shaped like a UUID:
// account-less handles are minted with dashes.
func TestAnAtPrefixAlwaysMeansHandle(t *testing.T) {
	var asked string
	rec := newRecorder(t, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/profiles/me" {
			return profilesHandler(w, r)
		}
		if strings.HasPrefix(r.URL.Path, "/profiles/") {
			asked = r.URL.Path
			w.Write([]byte(`{"id":"` + editorID + `"}`))
			return true
		}
		w.WriteHeader(http.StatusNotFound)
		return true
	})

	doc := strings.Replace(lectureDoc, `"@ivanov-ii-a1b2c3"`, `"@`+lecturerID+`"`, 1)
	path := writeFile(t, t.TempDir(), "01.md", doc)
	publishOne(context.Background(), api.New(rec.server.URL, "tok", "test"), path, true, nil)

	if asked != "/profiles/"+lecturerID {
		t.Errorf("an @-prefixed value was not looked up as a handle (asked %q)", asked)
	}
}
