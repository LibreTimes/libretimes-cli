// HTTP status -> user-facing error, and the one mapping that is a security
// control.
//
// # The 404 rule
//
// The platform answers 404 rather than 403 for a resource the caller may not
// see, so that the resource's existence is never leaked. Private profiles,
// draft publications and owner-only CV sections all rely on it.
//
// That contract has to survive the translation into a CLI message. If lt says
// "you lack access" where the API said "not found", it becomes an existence
// oracle: ask for a handle, and the wording of the refusal tells you whether
// the account exists. So NotFoundText is a single constant, used for every 404
// with no interpolation of any kind — no id, no handle, no path, no upstream
// detail. Two callers, one who lacks access and one asking for something
// genuinely absent, get byte-identical text.
//
// This is why detailOf is never consulted on a 404. The upstream body may well
// explain which of the two happened; forwarding it is the leak.
//
// Go has no sum types to enforce this, so the discipline is structural
// instead: FromResponse is the only constructor of an *Error carrying a status,
// and every response in this package funnels through it.
package api

import (
	"fmt"
	"strings"
)

// NotFoundText is one constant, no interpolation. See the package doc —
// varying this text is an information leak, not a UX improvement.
const NotFoundText = "Not found. Either it does not exist, or it is not visible to you. " +
	"These are deliberately indistinguishable."

// Error codes. Stable machine-readable slugs; --json emits them, so treat them
// as part of the CLI's contract.
const (
	CodeNotFound           = "not_found"
	CodeUnauthenticated    = "unauthenticated"
	CodeSessionRevoked     = "session_revoked"
	CodeOnboardingRequired = "onboarding_required"
	CodeForbidden          = "forbidden"
	CodeConflict           = "conflict"
	CodeValidation         = "validation_error"
	CodeRateLimited        = "rate_limited"
	CodeUpstream           = "upstream_error"
	CodeHTTP               = "http_error"
	CodeTimeout            = "timeout"
	CodeNetwork            = "network_error"
)

// Error is a request that reached the API and was refused, or failed in
// transit. The message is written to be read by a person or acted on by an
// agent, not to be parsed.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

// IsNotFound reports whether err is the 404 mapping. Callers use this to turn
// "no such row" into a local decision — publish does, to tell a first import
// from a re-import — without ever inspecting the message text.
func IsNotFound(err error) bool {
	e, ok := err.(*Error)
	return ok && e.Code == CodeNotFound
}

// FromResponse maps an HTTP failure onto an *Error.
//
// context is a short hint about the operation ("create publication"), used only
// where it cannot leak anything — never on a 404.
func FromResponse(status int, body []byte, context string) *Error {
	if status == 404 {
		// No detail, no context. Byte-identical for both causes, always.
		return &Error{Code: CodeNotFound, Message: NotFoundText}
	}

	detail := detailOf(body)

	switch {
	case status == 401:
		// SESSION_REVOKED is not an expiry, and the ordinary advice is wrong
		// for it. The revocation is keyed on the Keycloak session id, and a
		// refresh reuses that session — so every refreshed token carries the
		// same dead `sid` and is refused identically. Worse, a plain
		// `lt auth login` re-uses the browser's existing SSO cookie, mints a
		// token on that same session, and fails again: the obvious recovery
		// loops. Only a new session helps, which is what --force asks for.
		if strings.Contains(strings.ToUpper(detail+" "+codeOf(body)), "SESSION_REVOKED") {
			return &Error{CodeSessionRevoked,
				"This session was signed out server-side, so every token refreshed " +
					"from it is refused. Refreshing cannot fix this, and a plain " +
					"`lt auth login` may silently reuse the same session.\n" +
					"Start a new one:\n\n    lt auth login --force"}
		}
		return &Error{CodeUnauthenticated,
			"The token was rejected. It is missing, expired, or was revoked. " +
				"Run `lt auth login` and retry."}

	case status == 403:
		// ONBOARDING_REQUIRED is its own thing and must not read as a
		// permission problem: the caller is authenticated but has no Profile
		// yet, so no retry and no re-login helps. Only completing onboarding
		// does.
		haystack := strings.ToUpper(detail + " " + codeOf(body))
		if strings.Contains(haystack, "ONBOARDING_REQUIRED") {
			return &Error{CodeOnboardingRequired,
				"This account is signed in but has no LibreTimes profile yet, so " +
					"it cannot act. Finish onboarding at " +
					"https://libretimes.io/welcomes-you first. Retrying will not help."}
		}
		return &Error{CodeForbidden, or(detail,
			"This account is not permitted to perform that action. "+
				"Retrying will not help.")}

	case status == 409:
		return &Error{CodeConflict, or(detail,
			"That conflicts with something that already exists. If you are "+
				"importing, an entry with this import_key already exists — look it "+
				"up and update it instead of creating a second one.")}

	case status == 422:
		return &Error{CodeValidation, or(detail,
			"The arguments were rejected as invalid, and the API did not say "+
				"which field or why. Re-check the field types and permitted values.")}

	case status == 429:
		return &Error{CodeRateLimited, "Rate limited. Slow down and retry after a pause."}

	case status >= 500:
		// Never forward an upstream 5xx body: it is the most likely place for
		// a stack trace or an internal identifier to surface.
		msg := fmt.Sprintf("The API failed with a server error (%d)", status)
		if context != "" {
			msg += " during " + context
		}
		return &Error{CodeUpstream, msg + ". This is not a problem with the arguments; retry later."}
	}

	msg := fmt.Sprintf("The API returned %d", status)
	if detail != "" {
		msg += ": " + detail
	}
	return &Error{CodeHTTP, msg + "."}
}

func or(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}
