package auth

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s, err := NewStore()
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

// The refresh token must never exist on disk in a world-readable state, not
// even for the width of a write. 0600 is applied at creation, so this checks
// the mode of the file that was actually produced.
func TestCredentialFileIsOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are a no-op on Windows; %APPDATA% is user-scoped")
	}

	store := newTestStore(t)
	if err := store.Save(&Credential{
		Issuer: "https://auth.example/realms/r", ClientID: "c", RefreshToken: "secret",
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	info, err := os.Stat(store.Path())
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("credentials mode = %#o, want 0600", perm)
	}
}

// A file that somehow became group- or world-readable must be corrected on the
// next write, not left as it was found.
func TestSaveRepairsLoosePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission bits are a no-op on Windows")
	}

	store := newTestStore(t)
	cred := &Credential{Issuer: "https://auth.example/realms/r", ClientID: "c"}
	if err := store.Save(cred); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := os.Chmod(store.Path(), 0o644); err != nil {
		t.Fatalf("Chmod: %v", err)
	}

	if err := store.Save(cred); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	info, _ := os.Stat(store.Path())
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("mode after re-save = %#o, want 0600", perm)
	}
}

// The scoping is the whole point: signing into a dev stack must not evict a
// production login, and a prod command must not spend a dev token.
func TestCredentialsAreScopedByIssuerAndClient(t *testing.T) {
	store := newTestStore(t)

	prod := &Credential{Issuer: "https://auth.libretimes.io/realms/libretimes", ClientID: "libretimes-cli", AccessToken: "prod-token"}
	dev := &Credential{Issuer: "https://auth.libretimes.localhost/realms/libretimes", ClientID: "libretimes-cli", AccessToken: "dev-token"}

	if err := store.Save(prod); err != nil {
		t.Fatalf("Save prod: %v", err)
	}
	if err := store.Save(dev); err != nil {
		t.Fatalf("Save dev: %v", err)
	}

	got, err := store.Lookup(prod.Issuer, prod.ClientID)
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got == nil || got.AccessToken != "prod-token" {
		t.Fatalf("signing into dev evicted the prod login: %+v", got)
	}

	// A different client at the same issuer is also a different credential.
	other, err := store.Lookup(prod.Issuer, "some-other-client")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if other != nil {
		t.Errorf("a token for another client was returned: %+v", other)
	}
}

func TestDeleteRemovesOnlyTheOne(t *testing.T) {
	store := newTestStore(t)
	a := &Credential{Issuer: "iss-a", ClientID: "c", AccessToken: "a"}
	b := &Credential{Issuer: "iss-b", ClientID: "c", AccessToken: "b"}
	_ = store.Save(a)
	_ = store.Save(b)

	removed, err := store.Delete("iss-a", "c")
	if err != nil || !removed {
		t.Fatalf("Delete = %v, %v", removed, err)
	}

	if got, _ := store.Lookup("iss-a", "c"); got != nil {
		t.Error("deleted credential still present")
	}
	if got, _ := store.Lookup("iss-b", "c"); got == nil {
		t.Error("Delete removed an unrelated credential")
	}

	removed, err = store.Delete("iss-a", "c")
	if err != nil || removed {
		t.Errorf("second Delete = %v, %v; want false, nil", removed, err)
	}
}

// The v1 flat shape is what the Python importer wrote. Reading it means an
// existing login survives the upgrade.
func TestReadsLegacyFlatFormat(t *testing.T) {
	store := newTestStore(t)
	if err := os.MkdirAll(filepath.Dir(store.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := `{"issuer":"https://auth.libretimes.io/realms/libretimes",
	            "client_id":"libretimes-cli","access_token":"old","refresh_token":"r",
	            "expires_at":4102444800}`
	if err := os.WriteFile(store.Path(), []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := store.Lookup("https://auth.libretimes.io/realms/libretimes", "libretimes-cli")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got == nil || got.AccessToken != "old" {
		t.Fatalf("legacy credential not read: %+v", got)
	}
}

// A corrupt cache must send the user to `lt auth login`, not break the CLI.
func TestCorruptCacheIsTreatedAsEmpty(t *testing.T) {
	store := newTestStore(t)
	if err := os.MkdirAll(filepath.Dir(store.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.Path(), []byte("{not json at all"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := store.Lookup("iss", "c")
	if err != nil {
		t.Fatalf("a corrupt cache should not error: %v", err)
	}
	if got != nil {
		t.Errorf("got = %+v, want nil", got)
	}
}

// A version before this one wrote under the old, hand-computed path
// (effectively ~/.config on every platform, including Windows and macOS).
// NewStore now resolves via os.UserConfigDir, which moves that path on
// Windows and Darwin — this pins that an existing login there is still found
// rather than reading as "not signed in" the moment someone upgrades
// (WINDOWS.md #3). Constructed directly rather than through NewStore, which
// on Linux CI resolves both paths to the same place via XDG_CONFIG_HOME and
// so cannot exercise a genuine divergence.
func TestLoadFallsBackToTheLegacyPath(t *testing.T) {
	newDir := t.TempDir()
	oldDir := t.TempDir()
	store := &Store{
		path:       filepath.Join(newDir, "credentials.json"),
		legacyPath: filepath.Join(oldDir, "credentials.json"),
	}

	legacy := `{"version":2,"credentials":{"iss|c":{"issuer":"iss","client_id":"c","access_token":"old-path-token"}}}`
	if err := os.WriteFile(store.legacyPath, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := store.Lookup("iss", "c")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got == nil || got.AccessToken != "old-path-token" {
		t.Fatalf("legacy-path credential not read: %+v", got)
	}

	// A file at the new path takes precedence and the legacy one is never
	// consulted — this is a one-time fallback, not a permanent merge of two
	// caches.
	if err := os.MkdirAll(filepath.Dir(store.path), 0o700); err != nil {
		t.Fatal(err)
	}
	current := `{"version":2,"credentials":{"iss|c":{"issuer":"iss","client_id":"c","access_token":"new-path-token"}}}`
	if err := os.WriteFile(store.path, []byte(current), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err = store.Lookup("iss", "c")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got == nil || got.AccessToken != "new-path-token" {
		t.Fatalf("the current path should win once it exists: %+v", got)
	}
}

// Notepad and `Set-Content -Encoding utf8` under PowerShell 5.1 both add a
// UTF-8 BOM on save. json.Unmarshal rejects the leading bytes outright, so
// without stripping it a perfectly good cache reads as corrupt
// (WINDOWS.md #8) — the same failure TestCorruptCacheIsTreatedAsEmpty checks
// lt survives, but this file should not need to survive it: it should just
// load.
func TestBOMInCredentialsFileIsTolerated(t *testing.T) {
	store := newTestStore(t)
	if err := os.MkdirAll(filepath.Dir(store.Path()), 0o700); err != nil {
		t.Fatal(err)
	}
	withBOM := append([]byte("\xef\xbb\xbf"),
		[]byte(`{"version":2,"credentials":{"iss|c":{"issuer":"iss","client_id":"c","access_token":"tok"}}}`)...)
	if err := os.WriteFile(store.Path(), withBOM, 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := store.Lookup("iss", "c")
	if err != nil {
		t.Fatalf("a BOM should not error: %v", err)
	}
	if got == nil || got.AccessToken != "tok" {
		t.Fatalf("credential behind a BOM was not read: %+v", got)
	}
}

// A crash mid-write must never leave the real file truncated — the write
// goes to path+".tmp" and is renamed over the target, so path itself is
// either the old complete content or the new complete content, never a
// partial one. This cannot simulate the crash itself, but it does pin that a
// normal Save leaves no stray .tmp file behind and that the final content is
// exactly what was written (WINDOWS.md #7).
func TestSaveIsAtomicAndLeavesNoTempFile(t *testing.T) {
	store := newTestStore(t)
	if err := store.Save(&Credential{Issuer: "iss", ClientID: "c", AccessToken: "tok"}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if _, err := os.Stat(store.Path() + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("a .tmp file was left behind after Save: err = %v", err)
	}

	got, err := store.Lookup("iss", "c")
	if err != nil || got == nil || got.AccessToken != "tok" {
		t.Fatalf("Lookup after Save = %+v, %v", got, err)
	}
}

func TestSavedFileRoundTripsAsVersion2(t *testing.T) {
	store := newTestStore(t)
	_ = store.Save(&Credential{Issuer: "iss", ClientID: "c", ExpiresAt: float64(time.Now().Unix())})

	raw, err := os.ReadFile(store.Path())
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	if f.Version != currentVersion {
		t.Errorf("version = %d, want %d", f.Version, currentVersion)
	}
}
