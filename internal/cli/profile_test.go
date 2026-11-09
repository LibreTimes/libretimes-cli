package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A 1x1 PNG: real bytes, so the type is sniffed rather than taken from the name.
var tinyPNG = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x06\x00\x00\x00\x1f\x15\xc4\x89" +
	"\x00\x00\x00\rIDATx\x9cc\xf8\x0f\x00\x00\x01\x01\x00\x05\x18\xd8N\x00\x00\x00\x00IEND\xaeB`\x82")

func TestProfileImageSetUploadsWithYes(t *testing.T) {
	var gotType, gotFocus string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/account/profile-image" {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		_, header, err := r.FormFile("file")
		if err != nil {
			t.Fatalf("no file part: %v", err)
		}
		gotType, gotFocus = header.Header.Get("Content-Type"), r.FormValue("focus")
		w.Write([]byte(`{"profile_image_url":"https://assets.example/p.webp","width":512,"height":512}`))
	}))
	defer server.Close()

	// Named .jpg, but it is a PNG: the bytes decide.
	path := writeFile(t, t.TempDir(), "me.jpg", string(tinyPNG))
	out, stderr, code := runLT(t, server.URL, "profile", "image", "set", path, "--focus", "0,0.4", "--yes", "--json")
	if code != ExitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if gotType != "image/png" || gotFocus != "0,0.4" {
		t.Errorf("sent %s with focus %q", gotType, gotFocus)
	}
	var result profileImageResult
	if err := json.Unmarshal([]byte(out), &result); err != nil || result.Action != "set" ||
		result.ProfileImageURL == nil || result.Width != 512 {
		t.Errorf("stdout = %q (%v)", out, err)
	}
}

func TestProfileImageSetRefusesBeforeAnyRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("no request should be made, got %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()
	png := writeFile(t, t.TempDir(), "me.png", string(tinyPNG))

	cases := []struct {
		name string
		args []string
		code int
		want string
	}{
		{"needs --yes", []string{png}, ExitNeedsConfirm, "--yes"},
		{"crop and focus", []string{png, "--crop", "0,0,1,1", "--focus", "0,0", "--yes"}, ExitBadArguments, "not both"},
		{"not an image", []string{writeFile(t, t.TempDir(), "cv.pdf", "%PDF-1.7\n"), "--yes"}, ExitBadArguments, "JPEG, PNG"},
		{"dry run", []string{png, "--dry-run"}, ExitOK, "would upload"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, stderr, code := runLT(t, server.URL, append([]string{"profile", "image", "set"}, c.args...)...)
			if code != c.code || !strings.Contains(out+stderr, c.want) {
				t.Errorf("exit %d, want %d; output %q lacks %q", code, c.code, out+stderr, c.want)
			}
		})
	}
}
