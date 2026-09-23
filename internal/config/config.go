// Package config resolves which LibreTimes deployment lt talks to.
//
// One flag picks a whole deployment. Pointing the CLI at a dev stack used to
// mean exporting four variables that had to agree with each other, and getting
// one wrong produced a wrong-audience 401 that reads like a server fault rather
// than "you are pointed at the wrong stack".
package config

import (
	"fmt"
	"os"
	"strings"
)

// Profile is everything needed to reach one deployment: where the API is, and
// where to authenticate against it. The two travel together because a token
// from one realm is worthless at another's API, and the credential store keys
// on exactly this pair (see internal/auth.Store).
type Profile struct {
	// Name is the profile that was selected, for display only.
	Name string
	// APIBase is the product-path base URL, e.g. .../libretimes/v1.
	APIBase string
	// AuthURL is the Keycloak root, without a realm path.
	AuthURL string
	Realm   string
	// ClientID is a public client. There is no secret; a CLI on someone's
	// laptop cannot keep one, which is what PKCE exists to compensate for.
	ClientID string
}

// Issuer is the realm URL Keycloak stamps into every token it mints, and the
// key half of the credential store's identity for a login.
func (p Profile) Issuer() string {
	return strings.TrimSuffix(p.AuthURL, "/") + "/realms/" + p.Realm
}

func (p Profile) AuthorizationEndpoint() string {
	return p.Issuer() + "/protocol/openid-connect/auth"
}

func (p Profile) TokenEndpoint() string {
	return p.Issuer() + "/protocol/openid-connect/token"
}

// DeviceAuthorizationEndpoint is the RFC 8628 entry point. Keycloak only
// answers here when the client carries
// `oauth2.device.authorization.grant.enabled`; without it the request fails in
// a way worth explaining rather than relaying (see internal/auth/device.go).
func (p Profile) DeviceAuthorizationEndpoint() string {
	return p.Issuer() + "/protocol/openid-connect/auth/device"
}

const (
	// DefaultClientID is a public client registered with loopback redirect
	// URIs and PKCE S256 required. It is deliberately not the client the MCP
	// server uses: these tokens are spent at the API, whose validator demands
	// aud == libretimes-bff, while the agent client stamps the MCP resource
	// URL. One client serving both would have to carry both audiences, and a
	// credential valid at two resources is the confused-deputy hole the whole
	// design avoids.
	DefaultClientID = "libretimes-cli"
	DefaultRealm    = "libretimes"
)

// Product names. What a command targets, not a user-facing flag. Auth
// (issuer, realm, client id) never varies by product; only the API base does,
// which is why Product is threaded through Resolve and nothing else.
//
// There is one product today. LibreProblems had a second entry here until
// 2026-09-16: its sheets moved into libretimes.io as publications of type
// `problems` (2026-09-14) and problems-bff was parked, so `lt problems` had
// nothing left to talk to. A problem sheet is published with `lt publish`.
const (
	ProductLibreTimes = "libretimes"
)

// productRoute is enough to build one product's API base in one environment.
type productRoute struct {
	// prodPath is the path segment under api.libretimes.io -- every product
	// shares that one host (docs/architecture/domain-architecture.md).
	prodPath string
}

var productRoutes = map[string]productRoute{
	ProductLibreTimes: {prodPath: "libretimes/v1"},
}

var profiles = map[string]Profile{
	"prod": {
		Name:     "prod",
		AuthURL:  "https://auth.libretimes.io",
		Realm:    DefaultRealm,
		ClientID: DefaultClientID,
	},
	// The dev stack behind its proxy (`make compose-up-proxy`), which mirrors
	// prod: auth. and api. hosts, the same product paths, HTTPS with the mkcert
	// CA. Not the plain `compose up` ports: a proxied Keycloak's hostname is
	// the auth. host, so a sign-in started on localhost:8080 loses its session
	// cookie mid-flow, and the BFFs accept only that host's issuer (#1094).
	"dev": {
		Name:     "dev",
		AuthURL:  "https://auth.libretimes.localhost",
		Realm:    DefaultRealm,
		ClientID: DefaultClientID,
	},
}

// productAPIBase resolves one product's default API base in one environment.
// Both environments route by path under one API host, as prod does.
func productAPIBase(envName, product string) (string, error) {
	route, ok := productRoutes[product]
	if !ok {
		return "", fmt.Errorf("unknown product %q", product)
	}
	switch envName {
	case "prod":
		return "https://api.libretimes.io/" + route.prodPath, nil
	case "dev":
		return "https://api.libretimes.localhost/" + route.prodPath, nil
	default:
		return "", fmt.Errorf("unknown environment %q (known: dev, prod)", envName)
	}
}

// Overrides are the individually-settable pieces, from flags.
type Overrides struct {
	Env      string
	APIBase  string
	AuthURL  string
	Realm    string
	ClientID string
}

// Resolve builds the effective profile for one product.
//
// Precedence, most specific first: an explicit flag, then the matching
// environment variable, then the selected profile, then prod. The environment
// variables are the ones agent/ has always honoured, so anything already
// scripted against the Python lt keeps working unchanged. There is one
// `--api-url` / `LIBRETIMES_API_BASE_URL` override, not one per product: only
// one product is ever in play for a given invocation, so "override the base
// this command uses" is unambiguous without a second variable to remember.
func Resolve(o Overrides, product string) (Profile, error) {
	name := firstNonEmpty(o.Env, os.Getenv("LT_ENV"), "prod")
	base, ok := profiles[name]
	if !ok {
		return Profile{}, fmt.Errorf("unknown environment %q (known: dev, prod)", name)
	}

	defaultAPIBase, err := productAPIBase(name, product)
	if err != nil {
		return Profile{}, err
	}

	base.APIBase = strings.TrimSuffix(
		firstNonEmpty(o.APIBase, os.Getenv("LIBRETIMES_API_BASE_URL"), defaultAPIBase), "/")
	base.AuthURL = strings.TrimSuffix(
		firstNonEmpty(o.AuthURL, os.Getenv("LIBRETIMES_AUTH_URL"), base.AuthURL), "/")
	base.Realm = firstNonEmpty(o.Realm, os.Getenv("LIBRETIMES_REALM"), base.Realm)
	base.ClientID = firstNonEmpty(o.ClientID, os.Getenv("LIBRETIMES_CLIENT_ID"), base.ClientID)

	return base, nil
}

// TokenOverride is a bearer token supplied directly, bypassing the credential
// store entirely. It stays supported for one-off use against another account,
// which is how the tool layer was driven before `lt login` existed.
func TokenOverride() string {
	return os.Getenv("LIBRETIMES_TOKEN")
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
