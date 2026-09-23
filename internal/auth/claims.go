package auth

import (
	"encoding/base64"
	"encoding/json"
	"strings"
)

// UsernameFromToken reads `preferred_username` out of an access token, for
// display only.
//
// The signature is NOT verified, and that is correct here rather than a
// shortcut: this token is one lt already holds in its own credential cache and
// is about to spend, and nothing is being authorized on the answer. The label
// exists so a person can see *which account* a cached credential belongs to
// before running a command that writes as it. Every real trust decision is
// still made by the API, which does verify.
//
// Deliberately reads only the public handle. `sub` is the Keycloak identifier
// and never leaves the auth boundary — it must not be logged, displayed, or
// used as a key (a platform rule), so it is not read here at all.
//
// Returns "" for anything unparseable. A missing label is a cosmetic loss; an
// error here would break `lt auth status` over a token shape that is none of
// its business.
func UsernameFromToken(accessToken string) string {
	parts := strings.Split(accessToken, ".")
	if len(parts) != 3 {
		return ""
	}
	// JWT uses base64url with no padding.
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		PreferredUsername string `json:"preferred_username"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return ""
	}
	return claims.PreferredUsername
}
