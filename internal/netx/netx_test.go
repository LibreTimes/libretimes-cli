package netx

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLoopback(t *testing.T) {
	cases := map[string]string{
		"auth.libretimes.localhost:443": "127.0.0.1:443",
		"localhost:8080":                "127.0.0.1:8080",
		"API.LibreTimes.Localhost.:443": "127.0.0.1:443",
		"api.libretimes.io:443":         "api.libretimes.io:443",
		"evil-localhost:443":            "evil-localhost:443",
		"localhost.example.com:443":     "localhost.example.com:443",
		"127.0.0.1:8031":                "127.0.0.1:8031",
		"[::1]:8031":                    "[::1]:8031",
	}
	for in, want := range cases {
		if got := Loopback(in); got != want {
			t.Errorf("Loopback(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUntrustedNamesAnUnknownCA(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	// A client that trusts only the system roots, as lt does: the test
	// server's own CA is not among them.
	client := &http.Client{Transport: Transport()}
	_, err := client.Get(srv.URL)
	if err == nil || !Untrusted(err) {
		t.Fatalf("err = %v, want an untrusted-certificate error", err)
	}
	if Untrusted(errors.New("connection refused")) {
		t.Error("a plain network error is not a certificate error")
	}
}
