// Package netx holds the one piece of dialing policy every HTTP client in lt
// shares: `.localhost` names are loopback.
//
// RFC 6761 §6.3 reserves `localhost.` and everything under it for loopback,
// and browsers and curl resolve such names themselves without asking DNS. lt
// is built with CGO_ENABLED=0, so it uses Go's own resolver, which does ask
// DNS -- and a public resolver has no answer for `auth.libretimes.localhost`.
// The dev stack's proxy hosts sign-in there, so without this a browser signs
// in fine and lt then cannot reach the same host to collect the token.
package netx

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"strings"
)

// DialContext is the dialer's DialContext with `.localhost` names mapped to
// 127.0.0.1. Only the dial address changes: TLS still verifies the request's
// own host name, so a certificate for `*.libretimes.localhost` is checked as
// that name.
func DialContext(dial func(ctx context.Context, network, addr string) (net.Conn, error)) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		return dial(ctx, network, Loopback(addr))
	}
}

// Loopback rewrites a host:port whose host is `localhost` or under it.
func Loopback(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	name := strings.ToLower(strings.TrimSuffix(host, "."))
	if name == "localhost" || strings.HasSuffix(name, ".localhost") {
		return net.JoinHostPort("127.0.0.1", port)
	}
	return addr
}

// Transport is http.DefaultTransport's settings with the loopback rule, for
// the clients that have no transport tuning of their own.
func Transport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DialContext = DialContext((&net.Dialer{}).DialContext)
	return t
}

// Untrusted reports whether err is a TLS certificate the machine does not
// trust -- the dev stack without its mkcert CA installed, most often. Worth
// its own message: "could not reach" sends a person to check a server that
// is answering fine.
func Untrusted(err error) bool {
	var verify *tls.CertificateVerificationError
	var authority x509.UnknownAuthorityError
	var hostname x509.HostnameError
	return errors.As(err, &verify) || errors.As(err, &authority) || errors.As(err, &hostname)
}

// UntrustedMessage is the one sentence all three clients give for it.
const UntrustedMessage = "the server's TLS certificate is not trusted by this machine. " +
	"For the dev stack, install its mkcert CA (`mkcert -install`; see the LibreTimes " +
	"repo's docs/development/local-proxy.md)"
