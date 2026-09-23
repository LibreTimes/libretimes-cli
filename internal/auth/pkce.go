package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/The-LibreTimes/libretimes-cli/internal/config"
)

// Why this shape:
//
// Public client, PKCE, loopback redirect. A CLI on someone's laptop cannot
// keep a secret, so there is none to keep: PKCE is what stops an intercepted
// authorization code being redeemed by anyone else. The redirect goes to
// 127.0.0.1 on an ephemeral port (RFC 8252) because a native app has no fixed
// callback URL — the client is registered with a wildcard port for exactly
// this.
//
// The refresh token is what gets stored, not the access token. Access tokens
// live five minutes; storing one would mean logging in every five minutes.

// loginTimeout: the browser has to open, a human has to log in, and some
// realms add MFA. Two minutes is long enough for that and short enough that a
// forgotten terminal does not hold a listening socket open all day.
const loginTimeout = 2 * time.Minute

// DefaultScopes is enough for the flow and nothing more. `openid` is what
// makes this OIDC rather than bare OAuth; `offline_access` is what makes the
// refresh token outlive the browser session, which is the whole point of
// caching one.
var DefaultScopes = []string{"openid", "profile", "offline_access"}

// LoginBrowser runs the authorization-code + PKCE flow against a loopback
// listener and returns the resulting credential.
//
// force sends `prompt=login`, which makes Keycloak re-authenticate the user
// instead of silently reusing the browser's existing SSO session. That
// distinction is not cosmetic: session revocation is keyed on the Keycloak
// session id, a token refresh keeps the same `sid`, and an ordinary re-login
// rides the same SSO cookie — so after a server-side sign-out, every one of
// those paths mints another token carrying the same dead `sid`. A new session
// is the only thing that recovers, and this is how to ask for one.
func LoginBrowser(ctx context.Context, p config.Profile, scopes []string, force bool, out io.Writer) (*Credential, error) {
	verifier, challenge, err := pkcePair()
	if err != nil {
		return nil, err
	}
	state, err := randomString(32)
	if err != nil {
		return nil, err
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("could not open a loopback port for the redirect: %w", err)
	}
	defer listener.Close()

	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/callback", listener.Addr().(*net.TCPAddr).Port)

	query := url.Values{
		"client_id":             {p.ClientID},
		"response_type":         {"code"},
		"redirect_uri":          {redirectURI},
		"scope":                 {strings.Join(scopes, " ")},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	if force {
		query.Set("prompt", "login")
	}
	authorizeURL := p.AuthorizationEndpoint() + "?" + query.Encode()

	results := make(chan url.Values, 1)
	server := &http.Server{
		Handler:           callbackHandler(results),
		ReadHeaderTimeout: 10 * time.Second,
		// Silence. The default writes every request to stderr, and this
		// request's path carries the authorization code.
		ErrorLog: log.New(io.Discard, "", 0),
	}
	go func() { _ = server.Serve(listener) }()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	fmt.Fprintf(out, "Opening your browser to sign in to LibreTimes.\n\n  %s\n\n", authorizeURL)
	fmt.Fprintln(out, "If the browser did not open, paste that URL into it.")
	openBrowser(authorizeURL)

	waitCtx, cancel := context.WithTimeout(ctx, loginTimeout)
	defer cancel()

	var params url.Values
	select {
	case params = <-results:
	case <-waitCtx.Done():
		return nil, fmt.Errorf(
			"no response from the browser within %s. Run `lt auth login` again, "+
				"or use `lt auth login --device` if this machine has no browser",
			loginTimeout)
	}

	if errCode := params.Get("error"); errCode != "" {
		desc := params.Get("error_description")
		if desc == "" {
			desc = "no description"
		}
		return nil, fmt.Errorf("Keycloak returned %s: %s", errCode, desc)
	}

	// Constant-time, and checked before the code is spent: a mismatched state
	// is a CSRF attempt, not a retryable hiccup.
	if subtle.ConstantTimeCompare([]byte(params.Get("state")), []byte(state)) != 1 {
		return nil, errors.New("the redirect's `state` did not match. Login aborted")
	}

	code := params.Get("code")
	if code == "" {
		return nil, errors.New("the redirect carried no authorization code")
	}

	tokens, err := postForm(ctx, p.TokenEndpoint(), url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {p.ClientID},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"code_verifier": {verifier},
	})
	if err != nil {
		return nil, explainTokenError(err, p)
	}
	return credentialFrom(p, tokens), nil
}

// callbackHandler catches the one redirect and shows the user something human.
//
// The page itself is in callback_page.go. It is rendered into a buffer before
// anything is written, so a template failure becomes a 500 rather than a
// half-drawn page with a 200 already committed to the wire.
func callbackHandler(results chan<- url.Values) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		params := r.URL.Query()
		select {
		case results <- params:
		default:
			// Already handled one; a favicon request or a reload must not
			// block or overwrite the real result.
		}

		view := viewFor(params)
		status := http.StatusOK
		if !view.OK {
			status = http.StatusBadRequest
		}

		var page bytes.Buffer
		if err := callbackPage.Execute(&page, view); err != nil {
			// Unreachable short of a code change: the template is parsed at
			// init and the data has no methods to fail in. The terminal is
			// the authoritative channel either way — the flow has already
			// been handed the params above and carries on regardless.
			http.Error(w, "Sign-in handled. Return to your terminal.",
				http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// The URL in the address bar carries the authorization code. Nothing
		// should cache this response, and no referrer should carry it onward.
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(status)
		_, _ = w.Write(page.Bytes())
	})
}

// pkcePair returns a verifier and its S256 challenge, base64url with no
// padding (RFC 7636).
func pkcePair() (verifier, challenge string, err error) {
	verifier, err = randomString(64)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

func randomString(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("reading random bytes: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// openBrowser is best-effort. The URL is printed first, so a failure here
// costs the user a copy-paste rather than the login.
func openBrowser(target string) {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "windows":
		cmd, args = "rundll32", []string{"url.dll,FileProtocolHandler"}
	default:
		cmd = "xdg-open"
	}
	_ = exec.Command(cmd, append(args, target)...).Start()
}

// explainTokenError turns Keycloak's terser refusals into something that names
// the likely cause. `invalid_client` in particular means the realm has no such
// client, which is a provisioning gap rather than anything the user did.
func explainTokenError(err error, p config.Profile) error {
	var oe *oauthError
	if !errors.As(err, &oe) {
		return err
	}
	if oe.Code == "invalid_client" || oe.Code == "unauthorized_client" {
		return fmt.Errorf(
			"Keycloak rejected the client %q at %s (%s).\n"+
				"This usually means the realm has not been provisioned with it yet, "+
				"rather than anything wrong on this machine.",
			p.ClientID, p.Issuer(), oe.Code)
	}
	return err
}
