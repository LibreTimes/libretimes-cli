package auth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// serve runs one request through the real handler and returns the response.
func serve(t *testing.T, query string) (*http.Response, string) {
	t.Helper()

	results := make(chan url.Values, 1)
	recorder := httptest.NewRecorder()
	callbackHandler(results).ServeHTTP(recorder,
		httptest.NewRequest(http.MethodGet, "/callback?"+query, nil))

	resp := recorder.Result()
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp, recorder.Body.String()
}

func TestCallbackPageSuccess(t *testing.T) {
	resp, body := serve(t, "code=abc&state=xyz")

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Content-Type"); got != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}
	// The address bar holds the authorization code; the response must not be
	// cached and the referrer must not carry it onward.
	if got := resp.Header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
	if got := resp.Header.Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy = %q, want no-referrer", got)
	}
	for _, want := range []string{"Signed in to LibreTimes", "LibreTimes", "<!doctype html>"} {
		if !strings.Contains(body, want) {
			t.Errorf("page does not contain %q", want)
		}
	}
	// Nothing may be fetched from a third party while an authorization code
	// is in the address bar — that is the whole reason this page is inline.
	for _, forbidden := range []string{"http://", "https://", "//fonts."} {
		if strings.Contains(body, forbidden) {
			t.Errorf("page references an external resource (%q)", forbidden)
		}
	}
	// The code itself must never be echoed into the page.
	if strings.Contains(body, "abc") {
		t.Error("page echoes the authorization code")
	}
}

func TestCallbackPageFailure(t *testing.T) {
	resp, body := serve(t, "error=access_denied&state=xyz")

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if !strings.Contains(body, "Sign-in failed") {
		t.Error("page does not say the sign-in failed")
	}
	if !strings.Contains(body, "access_denied") {
		t.Error("page does not name the OAuth error code")
	}
}

// A redirect carrying neither a code nor an error is malformed, and rendering
// it as success would send the user back to a terminal still waiting.
func TestCallbackPageEmptyRedirectIsAFailure(t *testing.T) {
	resp, body := serve(t, "state=xyz")

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if !strings.Contains(body, "no authorization code") {
		t.Errorf("page does not explain the empty redirect:\n%s", body)
	}
}

// The error slug comes from the query string, so anyone who can get a user to
// open a URL controls it. html/template's contextual escaping is what stands
// between showing markup and running it, on a page whose origin has just
// handled an authorization code.
func TestCallbackPageEscapesTheErrorSlug(t *testing.T) {
	_, body := serve(t, url.Values{
		"error": {`<img src=x onerror="alert(1)">`},
	}.Encode())

	if strings.Contains(body, "<img src=x") {
		t.Fatalf("the error slug was not escaped:\n%s", body)
	}
	if !strings.Contains(body, "&lt;img") {
		t.Errorf("the error slug does not appear escaped either:\n%s", body)
	}
}

// A slug long enough to deface the page is not an OAuth error code.
func TestCallbackPageBoundsTheErrorSlug(t *testing.T) {
	_, body := serve(t, url.Values{"error": {strings.Repeat("x", 500)}}.Encode())

	if strings.Contains(body, strings.Repeat("x", 200)) {
		t.Error("the error slug was not truncated")
	}
	if !strings.Contains(body, "…") {
		t.Error("the truncation is not marked")
	}
}

// The handler hands the flow its params before rendering anything, so a
// browser that never renders still completes the login.
func TestCallbackHandlerDeliversParamsOnce(t *testing.T) {
	results := make(chan url.Values, 1)
	handler := callbackHandler(results)

	for range 2 {
		handler.ServeHTTP(httptest.NewRecorder(),
			httptest.NewRequest(http.MethodGet, "/callback?code=abc&state=xyz", nil))
	}

	got := <-results
	if got.Get("code") != "abc" {
		t.Fatalf("code = %q, want abc", got.Get("code"))
	}
	// A reload or a favicon request must not block, nor overwrite the result.
	select {
	case extra := <-results:
		t.Fatalf("a second request was delivered: %v", extra)
	default:
	}
}
