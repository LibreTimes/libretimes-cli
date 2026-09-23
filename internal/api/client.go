// Package api is the only way lt talks to LibreTimes: the public REST API.
//
// There is no domain-service client here and there must never be one. lt holds
// no internal secret, sets no X-Profile-Id, and reaches nothing a third party
// could not reach with the same token. If it were compromised it could do
// exactly what the presented token allows and nothing more — which is the
// property that keeps it outside the auth boundary entirely, and the property
// that makes it safe to open-source.
//
// Wire format is snake_case in both directions, untouched. No case conversion,
// no reshaping; every struct field carries an explicit json tag rather than
// relying on Go's capitalisation.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/The-LibreTimes/libretimes-cli/internal/netx"
)

// Per-phase timeouts, sized for a CLI rather than for a service, and derived
// from measurement rather than picked.
//
// These deliberately do NOT match the platform's service-to-service convention
// (connect 2s, read 10s). That convention is for one container calling another
// over a datacenter network, where 2s to connect really does mean something is
// wrong. lt runs on a laptop — behind a VPN, tethered to a phone, inside a cold
// CI container — and copying the datacenter numbers made it report "Could not
// reach the LibreTimes API" against an API that was working fine.
//
// Measured against production over a VPN, 2026-08-24, n=20 across the four
// endpoints lt calls, largest response 390 KB:
//
//	phase     median    p90     max    worst seen
//	connect     1.02   1.31    2.14    3.7
//	tls         ~0.5   ~0.8    1.7     -
//	ttfb        2.01   3.18    4.68    18.2
//	total       3.63   5.25    6.31    -
//
// The p90 is not the number to design to — it fails one request in ten on a
// healthy API, which is exactly the bug being fixed. Each budget is roughly 4x
// the worst value actually observed, so a request has to be genuinely stuck,
// not merely slow, before lt gives up. The 18.2s time-to-first-byte is the
// interesting one: that was a server-side stall on a request that then
// succeeded, and any read budget under ~20s turns it into a spurious failure.
//
// Set LT_TIMEOUT_SECONDS to scale all four for a worse link.
//
// httpx's pool-acquire timeout has no net/http equivalent — Go blocks on
// MaxConnsPerHost instead, which is unset here, so there is nothing to wait
// for. Noted rather than faked.
const (
	defaultConnectTimeout = 15 * time.Second  // ~4x the 3.7s worst observed
	defaultTLSHandshake   = 15 * time.Second  // handshake alone peaked at 1.7s
	defaultReadTimeout    = 60 * time.Second  // ~3x the 18.2s worst observed
	defaultOverallTimeout = 180 * time.Second // slow link + a large body

	idleConnTimeout  = 30 * time.Second
	maxIdleConns     = 8
	expectContinue   = 1 * time.Second
	userAgentProduct = "lt"
)

// timeoutScale reads LT_TIMEOUT_SECONDS and returns a multiplier applied to
// every phase budget.
//
// It is expressed against the overall budget so one number moves all four
// together and they cannot end up in a nonsensical order — a connect timeout
// longer than the overall deadline is not a configuration anyone means.
// Garbage, zero and negative values fall back to the defaults rather than
// producing a client that times out instantly.
func timeoutScale() float64 {
	raw := strings.TrimSpace(os.Getenv("LT_TIMEOUT_SECONDS"))
	if raw == "" {
		return 1
	}
	seconds, err := strconv.ParseFloat(raw, 64)
	// NaN and Inf parse successfully, and `NaN <= 0` is false — so without the
	// finiteness check "NaN" sails past the guard and every budget becomes an
	// undefined Duration. ParseFloat accepting them is easy to forget.
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 {
		return 1
	}
	return seconds / defaultOverallTimeout.Seconds()
}

func scaled(d time.Duration, factor float64) time.Duration {
	if factor == 1 {
		return d
	}
	out := time.Duration(float64(d) * factor)
	// A sub-second budget is never what someone meant, and it would make the
	// client fail before a healthy request could finish.
	if out < time.Second {
		return time.Second
	}
	return out
}

// Client is a thin wrapper over the public API. Every HTTP failure becomes an
// *Error, so callers never branch on a status code and can never accidentally
// surface a 404's upstream detail.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
	version string
}

// New builds a client for one deployment. token may be empty: reads of public
// data work anonymously, and that is deliberate — `lt` should be useful before
// anyone signs in.
func New(baseURL, token, version string) *Client {
	factor := timeoutScale()
	transport := &http.Transport{
		DialContext: netx.DialContext((&net.Dialer{
			Timeout:   scaled(defaultConnectTimeout, factor),
			KeepAlive: 30 * time.Second,
		}).DialContext),
		TLSHandshakeTimeout: scaled(defaultTLSHandshake, factor),
		// The read budget. Named for the response *headers* because that is
		// what net/http bounds; the body then streams under the overall
		// deadline below.
		ResponseHeaderTimeout: scaled(defaultReadTimeout, factor),
		ExpectContinueTimeout: expectContinue,
		MaxIdleConns:          maxIdleConns,
		IdleConnTimeout:       idleConnTimeout,
		ForceAttemptHTTP2:     true,
	}

	return &Client{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		token:   token,
		version: version,
		http: &http.Client{
			Transport: transport,
			Timeout:   scaled(defaultOverallTimeout, factor),
			// Never follow a redirect. A redirect chased with an
			// Authorization header attached is how a bearer token ends up at
			// a host that was never meant to see it.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// Authenticated reports whether a token was supplied.
func (c *Client) Authenticated() bool { return c.token != "" }

func (c *Client) Get(ctx context.Context, path string, query url.Values, out any) error {
	return c.do(ctx, http.MethodGet, path, query, nil, out)
}

func (c *Client) Post(ctx context.Context, path string, body, out any) error {
	return c.do(ctx, http.MethodPost, path, nil, body, out)
}

func (c *Client) Patch(ctx context.Context, path string, body, out any) error {
	return c.do(ctx, http.MethodPatch, path, nil, body, out)
}

func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encoding request body: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}

	target := c.baseURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	// Identifies the caller in logs. Not a credential.
	req.Header.Set("User-Agent", userAgentProduct+"/"+c.version+" (+https://libretimes.io)")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			return &Error{CodeTimeout,
				"The API did not respond in time. It may be briefly overloaded; " +
					"retry once before giving up."}
		}
		// No %w and no err text: net/http puts the full URL in its message,
		// and for a misconfigured base URL that can carry host details worth
		// not echoing.
		if netx.Untrusted(err) {
			return &Error{CodeNetwork, "The LibreTimes API: " + netx.UntrustedMessage + "."}
		}
		return &Error{CodeNetwork, "Could not reach the LibreTimes API."}
	}
	defer resp.Body.Close()

	// Cap the body. An error page from a misconfigured proxy can be
	// arbitrarily large, and nothing downstream needs more than this.
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return &Error{CodeNetwork, "The connection dropped while reading the response."}
	}

	if resp.StatusCode >= 400 {
		return FromResponse(resp.StatusCode, payload, describe(method, path))
	}

	if out == nil || resp.StatusCode == http.StatusNoContent || len(payload) == 0 {
		return nil
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return &Error{CodeUpstream,
			"The API returned a response that could not be decoded as JSON."}
	}
	return nil
}

// describe is the operation hint attached to 5xx messages. It is built from
// the method and a *static* path shape, never from the caller's arguments, so
// there is nothing in it to leak.
func describe(method, path string) string {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	if len(segments) == 0 {
		return strings.ToLower(method)
	}
	return strings.ToLower(method) + " " + segments[0]
}
