package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/The-LibreTimes/libretimes-cli/internal/config"
	"github.com/The-LibreTimes/libretimes-cli/internal/netx"
)

// expiryMargin refreshes slightly early rather than at the boundary: a token
// that expires mid-request fails the request, and the clock skew between here
// and Keycloak is not ours to control.
const expiryMargin = 30 * time.Second

// ErrNotSignedIn is returned when there is no usable credential. Callers turn
// this into exit code 4 and a message naming `lt auth login`.
var ErrNotSignedIn = errors.New("not signed in")

// tokenResponse is Keycloak's token endpoint payload.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	TokenType    string `json:"token_type"`
	Error        string `json:"error"`
	ErrorDesc    string `json:"error_description"`
}

// oauthError is a structured failure from the token or device endpoint. The
// device flow polls on specific codes, so the code has to survive transport
// rather than being flattened into prose.
type oauthError struct {
	Code        string
	Description string
	Status      int
}

func (e *oauthError) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Description)
	}
	return e.Code
}

// postForm posts an application/x-www-form-urlencoded request and decodes the
// token response. Used by every grant here.
func postForm(ctx context.Context, endpoint string, form url.Values) (*tokenResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("building the token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{
		Transport: netx.Transport(),
		Timeout:   30 * time.Second,
		// Same rule as the API client: never chase a redirect while carrying
		// credentials in the body.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		if netx.Untrusted(err) {
			return nil, fmt.Errorf("Keycloak at %s: %s", endpoint, netx.UntrustedMessage)
		}
		return nil, fmt.Errorf("could not reach Keycloak at %s", endpoint)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, errors.New("the connection dropped while talking to Keycloak")
	}

	var parsed tokenResponse
	// A non-JSON body from a proxy is still a failure worth reporting by
	// status, so decode errors are not fatal on the error path.
	_ = json.Unmarshal(body, &parsed)

	if resp.StatusCode >= 400 || parsed.Error != "" {
		code := parsed.Error
		if code == "" {
			code = fmt.Sprintf("http_%d", resp.StatusCode)
		}
		return nil, &oauthError{Code: code, Description: parsed.ErrorDesc, Status: resp.StatusCode}
	}
	if parsed.AccessToken == "" {
		return nil, errors.New("Keycloak returned no access token")
	}
	return &parsed, nil
}

// credentialFrom turns a token response into a storable credential.
func credentialFrom(p config.Profile, t *tokenResponse) *Credential {
	return &Credential{
		Issuer:       p.Issuer(),
		ClientID:     p.ClientID,
		AccessToken:  t.AccessToken,
		RefreshToken: t.RefreshToken,
		ExpiresAt:    float64(time.Now().Add(time.Duration(t.ExpiresIn) * time.Second).Unix()),
	}
}

// AccessToken returns a usable access token for the profile, refreshing when
// the cached one has expired.
//
// Precedence: LIBRETIMES_TOKEN beats the cache, so a one-off against another
// account or stack still works without logging out. Returns ErrNotSignedIn
// rather than a bare failure when the user simply is not logged in — that is
// the ordinary case on a fresh machine, and the caller turns it into a message
// naming `lt auth login`.
func AccessToken(ctx context.Context, store *Store, p config.Profile) (string, error) {
	if override := config.TokenOverride(); override != "" {
		return override, nil
	}

	cred, err := store.Lookup(p.Issuer(), p.ClientID)
	if err != nil {
		return "", err
	}
	if cred == nil {
		return "", ErrNotSignedIn
	}

	if time.Now().Add(expiryMargin).Before(cred.Expiry()) {
		return cred.AccessToken, nil
	}

	if cred.RefreshToken == "" {
		return "", ErrNotSignedIn
	}

	refreshed, err := Refresh(ctx, p, cred.RefreshToken)
	if err != nil {
		// An expired or revoked refresh token is a normal end-of-session, not
		// an error to propagate: fall through to "run lt auth login".
		return "", ErrNotSignedIn
	}
	updated := credentialFrom(p, refreshed)
	if err := store.Save(updated); err != nil {
		return "", err
	}
	return updated.AccessToken, nil
}

// Refresh exchanges a refresh token for a new access token.
func Refresh(ctx context.Context, p config.Profile, refreshToken string) (*tokenResponse, error) {
	return postForm(ctx, p.TokenEndpoint(), url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {p.ClientID},
		"refresh_token": {refreshToken},
	})
}
