package api

import (
	"strings"
	"testing"
)

// The 404 rule is a security control, not a message-formatting preference.
// These tests exist so that a future edit "improving" the wording has to
// delete an assertion that says why it must not.
func TestNotFoundIsByteIdenticalWhateverTheBodySays(t *testing.T) {
	bodies := [][]byte{
		nil,
		[]byte(`{}`),
		[]byte(`{"detail":"Profile @kolmogorov exists but is private"}`),
		[]byte(`{"error":{"detail":"publication 019ff044-a997-7d53 is a draft"}}`),
		[]byte(`{"detail":[{"loc":["path","username"],"msg":"no such user"}]}`),
		[]byte(`<html><body>404 /profiles/secret-handle</body></html>`),
	}

	first := FromResponse(404, bodies[0], "get profiles").Message
	if first != NotFoundText {
		t.Fatalf("404 message = %q, want the constant", first)
	}

	for _, body := range bodies {
		got := FromResponse(404, body, "get profiles")
		if got.Message != first {
			t.Errorf("404 message varied with the body.\n got: %q\nwant: %q\nbody: %s",
				got.Message, first, body)
		}
		if got.Code != CodeNotFound {
			t.Errorf("404 code = %q, want %q", got.Code, CodeNotFound)
		}
	}
}

// A 404 must not leak the caller's own arguments back either — the context
// hint is built from a static path shape, but this pins the contract at the
// mapping layer where it is enforced.
func TestNotFoundLeaksNoContext(t *testing.T) {
	err := FromResponse(404, []byte(`{"detail":"kolmogorov"}`), "get profiles/kolmogorov")
	for _, leak := range []string{"kolmogorov", "profiles", "404"} {
		if strings.Contains(err.Message, leak) {
			t.Errorf("404 message contains %q: %s", leak, err.Message)
		}
	}
}

// 403 ONBOARDING_REQUIRED is not a permission problem. Answering it as one
// sends the user to a login screen they pass and return from in the same
// state; only completing onboarding helps.
func TestOnboardingRequiredIsDistinctFromForbidden(t *testing.T) {
	cases := []struct {
		name string
		body []byte
	}{
		{"detail", []byte(`{"detail":"ONBOARDING_REQUIRED"}`)},
		{"nested code", []byte(`{"error":{"code":"ONBOARDING_REQUIRED","detail":"no profile"}}`)},
		{"lowercase", []byte(`{"detail":"onboarding_required"}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FromResponse(403, tc.body, "")
			if got.Code != CodeOnboardingRequired {
				t.Fatalf("code = %q, want %q", got.Code, CodeOnboardingRequired)
			}
			if !strings.Contains(got.Message, "welcomes-you") {
				t.Errorf("message should point at onboarding: %s", got.Message)
			}
		})
	}

	plain := FromResponse(403, []byte(`{"detail":"you are banned"}`), "")
	if plain.Code != CodeForbidden {
		t.Errorf("plain 403 code = %q, want %q", plain.Code, CodeForbidden)
	}
}

// A 5xx body is the most likely place for a stack trace or an internal
// identifier to surface, so it is never forwarded.
func TestServerErrorNeverForwardsTheBody(t *testing.T) {
	body := []byte(`{"detail":"psycopg2.OperationalError: FATAL: password authentication failed for user \"libretimes\" at 10.0.0.4:5432"}`)
	got := FromResponse(500, body, "create publication")

	for _, leak := range []string{"psycopg2", "password", "10.0.0.4", "libretimes\""} {
		if strings.Contains(got.Message, leak) {
			t.Errorf("5xx message leaked %q: %s", leak, got.Message)
		}
	}
	if got.Code != CodeUpstream {
		t.Errorf("code = %q, want %q", got.Code, CodeUpstream)
	}
	if !strings.Contains(got.Message, "create publication") {
		t.Errorf("5xx message should name the operation: %s", got.Message)
	}
}

// A 422 that says nothing is the failure mode that makes an agent retry blind.
// When the body does carry per-field errors, they must reach the caller.
func TestValidationErrorNamesTheField(t *testing.T) {
	body := []byte(`{"detail":[
		{"loc":["body","license"],"msg":"value is not a valid enumeration member"},
		{"loc":["body","type"],"msg":"field required"}
	]}`)
	got := FromResponse(422, body, "")

	if got.Code != CodeValidation {
		t.Fatalf("code = %q, want %q", got.Code, CodeValidation)
	}
	for _, want := range []string{"license", "type", "field required"} {
		if !strings.Contains(got.Message, want) {
			t.Errorf("message missing %q: %s", want, got.Message)
		}
	}
	// The "body" prefix FastAPI puts on every request-body error is noise.
	if strings.Contains(got.Message, "body.") {
		t.Errorf("message kept FastAPI's body prefix: %s", got.Message)
	}
}

func TestDetailShapes(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"nested error detail", `{"error":{"detail":"nope"}}`, "nope"},
		{"nested error message", `{"error":{"message":"nope"}}`, "nope"},
		{"error as string", `{"error":"nope"}`, "nope"},
		{"plain detail", `{"detail":"nope"}`, "nope"},
		{"message", `{"message":"nope"}`, "nope"},
		{"bare string", `"nope"`, "nope"},
		{"html", `<html>nope</html>`, ""},
		{"empty", ``, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := detailOf([]byte(tc.body)); got != tc.want {
				t.Errorf("detailOf(%s) = %q, want %q", tc.body, got, tc.want)
			}
		})
	}
}

func TestIsNotFound(t *testing.T) {
	if !IsNotFound(FromResponse(404, nil, "")) {
		t.Error("IsNotFound should recognise a 404 mapping")
	}
	if IsNotFound(FromResponse(409, nil, "")) {
		t.Error("IsNotFound matched a 409")
	}
	if IsNotFound(nil) {
		t.Error("IsNotFound matched nil")
	}
}

// The envelope the platform actually ships (libretimes_api_kit/errors.py):
// `error.detail` is an OBJECT carrying `error_code`, not prose. The original
// fixtures for ONBOARDING_REQUIRED all used the string form, so the
// discrimination looked tested while missing every real response.
func TestRealEnvelopeShapeIsRecognised(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   []byte
		want   string
	}{
		{
			// Verbatim, byte for byte, from api.libretimes.io.
			name:   "session revoked, live body",
			status: 401,
			body:   []byte(`{"error":{"status_code":401,"detail":{"error_code":"SESSION_REVOKED"},"type":"unauthorized"}}`),
			want:   CodeSessionRevoked,
		},
		{
			name:   "onboarding required, object detail",
			status: 403,
			body:   []byte(`{"error":{"status_code":403,"detail":{"error_code":"ONBOARDING_REQUIRED"},"type":"forbidden"}}`),
			want:   CodeOnboardingRequired,
		},
		{
			name:   "domain service answering directly",
			status: 403,
			body:   []byte(`{"detail":{"error_code":"ONBOARDING_REQUIRED"}}`),
			want:   CodeOnboardingRequired,
		},
		{
			// A plain expiry must NOT be mistaken for a revocation: the advice
			// differs, and --force is a pointless round trip for an expiry.
			name:   "ordinary 401 stays unauthenticated",
			status: 401,
			body:   []byte(`{"error":{"status_code":401,"detail":"token expired","type":"unauthorized"}}`),
			want:   CodeUnauthenticated,
		},
		{
			name:   "ordinary 403 stays forbidden",
			status: 403,
			body:   []byte(`{"error":{"status_code":403,"detail":"not your publication","type":"forbidden"}}`),
			want:   CodeForbidden,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FromResponse(tc.status, tc.body, "")
			if got.Code != tc.want {
				t.Fatalf("code = %q, want %q (message: %s)", got.Code, tc.want, got.Message)
			}
		})
	}
}

// The revocation message has to say the thing that is actually true: that a
// refresh cannot help and a plain re-login may not either.
func TestSessionRevokedExplainsWhyRetryingFails(t *testing.T) {
	got := FromResponse(401,
		[]byte(`{"error":{"status_code":401,"detail":{"error_code":"SESSION_REVOKED"},"type":"unauthorized"}}`), "")
	for _, want := range []string{"--force", "Refreshing cannot fix this"} {
		if !strings.Contains(got.Message, want) {
			t.Errorf("message missing %q: %s", want, got.Message)
		}
	}
}
