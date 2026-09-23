package auth

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func token(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

func TestUsernameFromToken(t *testing.T) {
	got := UsernameFromToken(token(t, map[string]any{
		"preferred_username": "kolmogorov",
		"sub":                "9f1c-not-to-be-shown",
	}))
	if got != "kolmogorov" {
		t.Errorf("got %q, want %q", got, "kolmogorov")
	}
}

// Unparseable input is a cosmetic loss, never an error: `lt auth status` must
// still list the deployment even if a token shape it does not model shows up.
func TestUsernameFromTokenIsForgiving(t *testing.T) {
	for _, tc := range []struct{ name, in string }{
		{"empty", ""},
		{"not a jwt", "just-a-string"},
		{"two segments", "header.payload"},
		{"four segments", "a.b.c.d"},
		{"payload not base64", "header.!!!not-base64!!!.sig"},
		{"payload not json", "header." + base64.RawURLEncoding.EncodeToString([]byte("nope")) + ".sig"},
		{"no username claim", token(t, map[string]any{"sub": "x"})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := UsernameFromToken(tc.in); got != "" {
				t.Errorf("got %q, want empty", got)
			}
		})
	}
}

// `sub` is the Keycloak identifier and does not leave the auth boundary. This
// helper reads one claim and must never start returning another.
func TestUsernameFromTokenNeverReturnsSub(t *testing.T) {
	const sub = "3f2504e0-4f89-11d3-9a0c-0305e82c3301"
	got := UsernameFromToken(token(t, map[string]any{
		"sub":                sub,
		"preferred_username": "kolmogorov",
	}))
	if strings.Contains(got, sub) {
		t.Fatalf("returned the sub: %q", got)
	}
	// And with no username present it returns nothing rather than falling back.
	if got := UsernameFromToken(token(t, map[string]any{"sub": sub})); got != "" {
		t.Fatalf("fell back to something with no preferred_username: %q", got)
	}
}
