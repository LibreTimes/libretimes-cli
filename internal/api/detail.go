package api

import (
	"encoding/json"
	"strings"
)

// detailOf pulls a human-readable detail out of whatever envelope came back.
//
// Deliberately tolerant, because the platform has more than one shape and the
// BFF relays each downstream body untouched:
//
//	{"error": {"detail": ...}}   profile-service and friends
//	{"detail": ...}              plain FastAPI
//	{"detail": [{...}, ...]}     FastAPI validation errors
//	{"message": ...}
//
// Never called for a 404. See the package doc.
func detailOf(body []byte) string {
	if len(body) == 0 {
		return ""
	}

	var raw any
	if err := json.Unmarshal(body, &raw); err != nil {
		// Not JSON — an HTML error page from a proxy, most likely. Returning
		// it would splice markup into a terminal message, and it is never the
		// useful half of the failure.
		return ""
	}

	if s, ok := raw.(string); ok {
		return s
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return ""
	}

	if errVal, present := obj["error"]; present {
		switch e := errVal.(type) {
		case map[string]any:
			for _, key := range []string{"detail", "message"} {
				if s, ok := e[key].(string); ok && s != "" {
					return s
				}
			}
		case string:
			if e != "" {
				return e
			}
		}
	}

	switch d := obj["detail"].(type) {
	case string:
		if d != "" {
			return d
		}
	case []any:
		if s := formatValidation(d); s != "" {
			return s
		}
	}

	if s, ok := obj["message"].(string); ok && s != "" {
		return s
	}
	return ""
}

// formatValidation turns FastAPI's validation list into something actionable.
//
// A bare {"detail": "Validation error"} tells a caller nothing, so it retries
// blind or gives up. When the upstream body does carry per-field errors, name
// the field and say what was wrong with it.
func formatValidation(items []any) string {
	var parts []string
	for _, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}

		field := ""
		switch loc := entry["loc"].(type) {
		case []any:
			var trail []string
			for _, p := range loc {
				// Drop the "body" prefix FastAPI puts on every
				// request-body error.
				if s := toString(p); s != "" && s != "body" {
					trail = append(trail, s)
				}
			}
			field = strings.Join(trail, ".")
		default:
			field = toString(entry["loc"])
		}
		if field == "" {
			field = toString(entry["field"])
		}

		msg := toString(entry["msg"])
		if msg == "" {
			msg = toString(entry["message"])
		}

		switch {
		case field != "" && msg != "":
			parts = append(parts, field+": "+msg)
		case msg != "":
			parts = append(parts, msg)
		}
	}
	return strings.Join(parts, "; ")
}

// codeOf reads the machine-readable error code out of the envelope, for the
// ONBOARDING_REQUIRED and SESSION_REVOKED discriminations in FromResponse.
//
// The platform's envelope is
//
//	{"error": {"status_code": 403, "detail": {...}, "type": "...", "code": "..."}}
//
// and `detail` is a STRING for ordinary refusals but an OBJECT carrying
// `error_code` for the ones that mean something specific — ONBOARDING_REQUIRED,
// SESSION_REVOKED, ACCOUNT_BANNED, DM_POLICY_*. That object form is the one
// that actually ships (libretimes_api_kit/errors.py), and reading only
// `error.code` silently missed every one of them: the discrimination fell
// through to the generic branch and the caller got advice for the wrong
// problem. Each shape below has been seen in a real response or a platform
// test, so all of them are read.
func codeOf(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		return ""
	}

	// {"error": {...}}
	if e, ok := obj["error"].(map[string]any); ok {
		if s := errorCodeIn(e["detail"]); s != "" {
			return s
		}
		if s, ok := e["code"].(string); ok && s != "" {
			return s
		}
	}
	// {"detail": {"error_code": ...}} — a domain service answering directly.
	if s := errorCodeIn(obj["detail"]); s != "" {
		return s
	}
	if s, ok := obj["code"].(string); ok {
		return s
	}
	return ""
}

// errorCodeIn pulls `error_code` out of a detail that is an object rather than
// prose. Returns "" for the string form, which callers handle via detailOf.
func errorCodeIn(detail any) string {
	obj, ok := detail.(map[string]any)
	if !ok {
		return ""
	}
	for _, key := range []string{"error_code", "code"} {
		if s, ok := obj[key].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func toString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		// JSON numbers land here; loc entries for list indices are numeric.
		return strings.TrimSuffix(strings.TrimRight(
			strings.TrimRight(jsonNumber(t), "0"), "."), ".")
	case nil:
		return ""
	default:
		return ""
	}
}

func jsonNumber(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}
