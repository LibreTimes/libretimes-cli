package api

import (
	"encoding/json"
	"net/http"
	"testing"
)

func decodeJSON(t *testing.T, r *http.Request, out any) {
	t.Helper()
	if err := json.NewDecoder(r.Body).Decode(out); err != nil {
		t.Fatalf("decoding request body: %v", err)
	}
}
