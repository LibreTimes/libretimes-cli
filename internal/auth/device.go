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

// The device authorization grant (RFC 8628).
//
// This is the flow for anywhere the browser flow cannot work: a container, an
// SSH session, a CI runner, a machine with no browser at all. lt prints a short
// URL and a user code; the person authorizes on whatever device they do have,
// and lt polls until that completes. Nothing listens on a port here, which is
// the whole point — the loopback redirect needs a browser on *this* machine.

// deviceAuthResponse is the RFC 8628 §3.2 payload.
type deviceAuthResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
	Error                   string `json:"error"`
	ErrorDesc               string `json:"error_description"`
}

// defaultPollInterval is RFC 8628's fallback when the server omits `interval`.
const defaultPollInterval = 5 * time.Second

// LoginDevice runs the device authorization grant and returns the resulting
// credential.
func LoginDevice(ctx context.Context, p config.Profile, scopes []string, force bool, out io.Writer) (*Credential, error) {
	auth, err := requestDeviceCode(ctx, p, scopes, force)
	if err != nil {
		return nil, err
	}

	fmt.Fprintf(out, "\n  First copy your one-time code: %s\n\n", auth.UserCode)
	if auth.VerificationURIComplete != "" {
		fmt.Fprintf(out, "  Then open: %s\n", auth.VerificationURIComplete)
		fmt.Fprintf(out, "  (or %s and enter the code)\n\n", auth.VerificationURI)
	} else {
		fmt.Fprintf(out, "  Then open: %s\n\n", auth.VerificationURI)
	}
	fmt.Fprintln(out, "Waiting for authorization...")

	interval := time.Duration(auth.Interval) * time.Second
	if interval <= 0 {
		interval = defaultPollInterval
	}

	lifetime := time.Duration(auth.ExpiresIn) * time.Second
	if lifetime <= 0 {
		lifetime = 10 * time.Minute
	}
	deadline, cancel := context.WithTimeout(ctx, lifetime)
	defer cancel()

	for {
		select {
		case <-deadline.Done():
			return nil, fmt.Errorf(
				"the code expired before it was authorized. Run `lt auth login --device` again")
		case <-time.After(interval):
		}

		tokens, err := postForm(deadline, p.TokenEndpoint(), url.Values{
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
			"client_id":   {p.ClientID},
			"device_code": {auth.DeviceCode},
		})
		if err == nil {
			return credentialFrom(p, tokens), nil
		}

		var oe *oauthError
		if !errors.As(err, &oe) {
			return nil, err
		}

		switch oe.Code {
		case "authorization_pending":
			// The ordinary case: the human has not finished yet.
			continue
		case "slow_down":
			// RFC 8628 §3.5 — back off by 5s and keep the new interval.
			interval += 5 * time.Second
		case "access_denied":
			return nil, errors.New("authorization was declined")
		case "expired_token":
			return nil, errors.New(
				"the code expired before it was authorized. Run `lt auth login --device` again")
		default:
			return nil, explainDeviceError(oe, p)
		}
	}
}

func requestDeviceCode(ctx context.Context, p config.Profile, scopes []string, force bool) (*deviceAuthResponse, error) {
	form := url.Values{
		"client_id": {p.ClientID},
		"scope":     {strings.Join(scopes, " ")},
	}
	// Same reasoning as LoginBrowser: the verification page runs in whatever
	// browser the user opens it in, and that browser may hold an SSO cookie
	// for the very session that was revoked.
	if force {
		form.Set("prompt", "login")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		p.DeviceAuthorizationEndpoint(), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, fmt.Errorf("building the device authorization request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{
		Transport: netx.Transport(),
		Timeout:   30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}

	resp, err := client.Do(req)
	if err != nil {
		if netx.Untrusted(err) {
			return nil, fmt.Errorf("Keycloak at %s: %s", p.DeviceAuthorizationEndpoint(), netx.UntrustedMessage)
		}
		return nil, fmt.Errorf("could not reach Keycloak at %s", p.DeviceAuthorizationEndpoint())
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, errors.New("the connection dropped while talking to Keycloak")
	}

	var parsed deviceAuthResponse
	_ = json.Unmarshal(body, &parsed)

	if resp.StatusCode >= 400 || parsed.Error != "" {
		code := parsed.Error
		if code == "" {
			code = fmt.Sprintf("http_%d", resp.StatusCode)
		}
		return nil, explainDeviceError(
			&oauthError{Code: code, Description: parsed.ErrorDesc, Status: resp.StatusCode}, p)
	}
	if parsed.DeviceCode == "" || parsed.UserCode == "" {
		return nil, errors.New("Keycloak returned an incomplete device authorization response")
	}
	return &parsed, nil
}

// explainDeviceError names the one failure that is a realm-provisioning gap
// rather than a user error.
//
// Keycloak only serves the device endpoint for a client carrying
// `oauth2.device.authorization.grant.enabled`. Without it the refusal is
// terse and reads like a bug in lt, so say what it actually is — this is
// operator work on the realm, and no amount of retrying fixes it.
func explainDeviceError(oe *oauthError, p config.Profile) error {
	switch oe.Code {
	case "invalid_client", "unauthorized_client", "invalid_request", "http_400", "http_401":
		return fmt.Errorf(
			"Keycloak refused the device authorization request for client %q at %s (%s).\n\n"+
				"The most likely cause is that the device grant is not enabled on that "+
				"client. It is a realm setting, not something this machine controls:\n"+
				"  OAuth 2.0 Device Authorization Grant -> On\n"+
				"  (attribute: oauth2.device.authorization.grant.enabled)\n\n"+
				"Until it is enabled, `lt auth login` without --device still works "+
				"anywhere a browser is reachable.",
			p.ClientID, p.Issuer(), oe.Code)
	}
	return oe
}
