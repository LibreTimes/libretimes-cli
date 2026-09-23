package api

import (
	"context"
	"encoding/json"
)

// Profile is the caller's own public identity, as GET /profiles/me returns it.
//
// Note what is absent and must stay absent: keycloak_id, the JWT `sub`, and
// anything else from the auth boundary. Those never appear in public API
// responses, and lt must never print one even if a future response carried it.
// profile_id is the cross-service identity key and is safe to show.
//
// The field names here are lt's output contract, not the wire's — see
// UnmarshalJSON for why the two differ.
type Profile struct {
	ProfileID   string `json:"profile_id"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	GivenName   string `json:"given_name"`
	FamilyName  string `json:"family_name"`
}

// UnmarshalJSON maps the ProfileMe wire shape onto the names lt emits.
//
// The API calls the identity key `id`, because within the profiles resource
// that is all it is. Everywhere else on the platform the same value is
// `profile_id` — it is what `X-Profile-Id` carries and what identifies a
// person across services — and lt talks about publications too, whose ids are
// also `id`. Emitting a bare `id` from a tool that handles both would be
// ambiguous exactly where it matters, so the rename is deliberate and lives
// here, in one place, rather than at each call site.
func (p *Profile) UnmarshalJSON(data []byte) error {
	// A distinct type, so this does not recurse back into UnmarshalJSON.
	var wire struct {
		ID          string `json:"id"`
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
		GivenName   string `json:"given_name"`
		FamilyName  string `json:"family_name"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	*p = Profile{
		ProfileID:   wire.ID,
		Username:    wire.Username,
		DisplayName: wire.DisplayName,
		GivenName:   wire.GivenName,
		FamilyName:  wire.FamilyName,
	}
	return nil
}

// Name is the most human label available, falling back down the chain rather
// than printing an empty string.
func (p Profile) Name() string {
	if p.DisplayName != "" {
		return p.DisplayName
	}
	if joined := trimJoin(p.GivenName, p.FamilyName); joined != "" {
		return joined
	}
	return p.Username
}

// Me returns the profile the current token belongs to.
func (c *Client) Me(ctx context.Context) (*Profile, error) {
	var p Profile
	if err := c.Get(ctx, "/profiles/me", nil, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

func trimJoin(a, b string) string {
	switch {
	case a != "" && b != "":
		return a + " " + b
	case a != "":
		return a
	default:
		return b
	}
}
