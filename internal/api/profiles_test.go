package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The ProfileMe wire shape, as the live API actually returns it. Trimmed to
// the fields lt reads, but the names are verbatim — the first version of
// Profile guessed `profile_id`/`first_name`/`last_name` and an `email` that
// does not exist, and the fixtures guessed identically, so every test passed
// while `lt whoami` printed an empty id against the real thing.
const profileMeWire = `{
  "id": "019f9073-76e7-7d32-b4f7-3a0b02a132bf",
  "username": "kolmogorov",
  "display_name": "Andrey",
  "given_name": "Andrey",
  "family_name": "Kolmogorov",
  "headline": "Probability",
  "visibility": {"is_public": true},
  "created_at": "2026-07-23T19:28:33.996655",
  "updated_at": "2026-08-04T19:35:36.736115"
}`

func TestProfileDecodesIDAsProfileID(t *testing.T) {
	var p Profile
	if err := json.Unmarshal([]byte(profileMeWire), &p); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if p.ProfileID != "019f9073-76e7-7d32-b4f7-3a0b02a132bf" {
		t.Errorf("ProfileID = %q, want the wire's `id`", p.ProfileID)
	}
	if p.Username != "kolmogorov" {
		t.Errorf("Username = %q", p.Username)
	}
	if p.GivenName != "Andrey" || p.FamilyName != "Kolmogorov" {
		t.Errorf("given/family = %q/%q", p.GivenName, p.FamilyName)
	}
}

// lt emits `profile_id`, not the wire's `id`. Agents parse this.
func TestProfileEmitsProfileID(t *testing.T) {
	var p Profile
	if err := json.Unmarshal([]byte(profileMeWire), &p); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	out, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var round map[string]any
	if err := json.Unmarshal(out, &round); err != nil {
		t.Fatalf("re-Unmarshal: %v", err)
	}
	if round["profile_id"] != "019f9073-76e7-7d32-b4f7-3a0b02a132bf" {
		t.Errorf("emitted profile_id = %v", round["profile_id"])
	}
	if _, ok := round["id"]; ok {
		t.Error("emitted a bare `id`; publication ids use that name too")
	}
	// Nothing from the auth boundary may ever appear.
	for _, banned := range []string{"keycloak_id", "sub", "email"} {
		if _, ok := round[banned]; ok {
			t.Errorf("emitted %q", banned)
		}
	}
}

func TestProfileNameFallsBack(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    Profile
		want string
	}{
		{"display name wins", Profile{DisplayName: "Andrey", GivenName: "A", Username: "k"}, "Andrey"},
		{"given + family", Profile{GivenName: "Andrey", FamilyName: "Kolmogorov", Username: "k"}, "Andrey Kolmogorov"},
		{"given only", Profile{GivenName: "Andrey", Username: "k"}, "Andrey"},
		{"family only", Profile{FamilyName: "Kolmogorov", Username: "k"}, "Kolmogorov"},
		{"username last", Profile{Username: "kolmogorov"}, "kolmogorov"},
	} {
		if got := tc.p.Name(); got != tc.want {
			t.Errorf("%s: Name() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// Me must survive the fields it does not model. The response carries twenty-odd
// keys and lt reads five; a stricter decode would break on the next one added.
func TestMeIgnoresUnmodelledFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(profileMeWire))
	}))
	defer server.Close()

	p, err := New(server.URL, "tok", "test").Me(context.Background())
	if err != nil {
		t.Fatalf("Me: %v", err)
	}
	if p.ProfileID == "" || p.Name() != "Andrey" {
		t.Errorf("got %+v", *p)
	}
}
