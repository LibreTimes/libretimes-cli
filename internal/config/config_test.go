package config

import "testing"

// The dev profile is the proxied stack, which mirrors prod host for host: an
// auth. host whose issuer the BFFs accept, and one api. host routed by path
// (#1094). A drift back to the plain compose ports breaks sign-in outright.
func TestResolveEnvironments(t *testing.T) {
	for _, v := range []string{"LT_ENV", "LIBRETIMES_API_BASE_URL", "LIBRETIMES_AUTH_URL",
		"LIBRETIMES_REALM", "LIBRETIMES_CLIENT_ID"} {
		t.Setenv(v, "")
	}
	cases := map[string]struct{ api, issuer string }{
		"prod": {"https://api.libretimes.io/libretimes/v1", "https://auth.libretimes.io/realms/libretimes"},
		"dev":  {"https://api.libretimes.localhost/libretimes/v1", "https://auth.libretimes.localhost/realms/libretimes"},
	}
	for env, want := range cases {
		p, err := Resolve(Overrides{Env: env}, ProductLibreTimes)
		if err != nil {
			t.Fatal(err)
		}
		if p.APIBase != want.api || p.Issuer() != want.issuer {
			t.Errorf("%s: api=%s issuer=%s, want %s %s", env, p.APIBase, p.Issuer(), want.api, want.issuer)
		}
	}
}

// The plain compose ports stay reachable, by override.
func TestOverridesReachThePlainComposePorts(t *testing.T) {
	t.Setenv("LIBRETIMES_API_BASE_URL", "")
	t.Setenv("LIBRETIMES_AUTH_URL", "")
	p, err := Resolve(Overrides{Env: "dev", APIBase: "http://localhost:8031/libretimes/v1/",
		AuthURL: "http://localhost:8080"}, ProductLibreTimes)
	if err != nil {
		t.Fatal(err)
	}
	if p.APIBase != "http://localhost:8031/libretimes/v1" || p.Issuer() != "http://localhost:8080/realms/libretimes" {
		t.Errorf("api=%s issuer=%s", p.APIBase, p.Issuer())
	}
}
