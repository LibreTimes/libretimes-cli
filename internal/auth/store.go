// Package auth owns the OAuth flows and the credential cache.
//
// What this deliberately does not do: no client secret, no password grant, and
// no reading of the user's credentials at any point. lt never sees them — the
// browser and Keycloak do. That is what keeps revocation, MFA and account
// state working: they belong to Keycloak, and these flows do not route around
// them.
package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Credential is one signed-in identity at one deployment.
type Credential struct {
	Issuer       string  `json:"issuer"`
	ClientID     string  `json:"client_id"`
	AccessToken  string  `json:"access_token"`
	RefreshToken string  `json:"refresh_token"`
	ExpiresAt    float64 `json:"expires_at"`
}

// Expiry returns the access token's expiry as a time.
func (c Credential) Expiry() time.Time {
	if c.ExpiresAt <= 0 {
		return time.Time{}
	}
	sec := int64(c.ExpiresAt)
	return time.Unix(sec, int64((c.ExpiresAt-float64(sec))*1e9))
}

// file is the on-disk shape.
//
// Version 2 keys credentials by issuer|client_id so a dev login and a prod
// login coexist. Version 1 — what the Python importer wrote — was a single
// flat object, so signing into a dev stack silently evicted a production
// login. v1 is still *read* (see load) precisely because that eviction is the
// bug being fixed, and losing a valid session while fixing it would be absurd.
type file struct {
	Version     int                    `json:"version"`
	Credentials map[string]*Credential `json:"credentials"`
}

const currentVersion = 2

// Store is the credential cache on disk.
type Store struct {
	path string
	// legacyPath is where this same cache lived before NewStore switched to
	// os.UserConfigDir() — read as a fallback so an existing login does not
	// silently become "not signed in" the moment someone upgrades. Empty
	// when it is unknowable (os.UserHomeDir failed) or identical to path
	// (Linux: os.UserConfigDir already resolves to the same place the old
	// hand-written logic did, so there is nothing to fall back from).
	legacyPath string
}

// NewStore locates the cache. os.UserConfigDir honours XDG_CONFIG_HOME on
// Unix already, resolves to %AppData% on Windows, and to
// ~/Library/Application Support on Darwin — three platform-correct answers
// from one stdlib call, in place of a hand-written one that always chose
// ~/.config and was simply wrong on Windows (WINDOWS.md #3: the README's own
// "%APPDATA%" claim was aspirational, not descriptive, until this).
func NewStore() (*Store, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("locating your config directory: %w", err)
	}
	path := filepath.Join(base, "libretimes", "credentials.json")

	var legacyPath string
	if oldBase := legacyConfigBase(); oldBase != "" {
		if candidate := filepath.Join(oldBase, "libretimes", "credentials.json"); candidate != path {
			legacyPath = candidate
		}
	}

	return &Store{path: path, legacyPath: legacyPath}, nil
}

// legacyConfigBase reproduces the pre-fix logic exactly, so `load` can fall
// back to reading whatever a version before this one actually wrote.
func legacyConfigBase() string {
	if base := os.Getenv("XDG_CONFIG_HOME"); base != "" {
		return base
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config")
}

// Path is where the cache lives, for display in messages.
func (s *Store) Path() string { return s.path }

func key(issuer, clientID string) string { return issuer + "|" + clientID }

// Lookup returns the credential for one issuer+client, or nil.
//
// The scoping is the point. A cache written against a different realm or
// client is not ours to spend: doing so produces a wrong-audience 401 that
// looks like a server fault rather than "you are pointed at a different
// stack".
func (s *Store) Lookup(issuer, clientID string) (*Credential, error) {
	f, err := s.load()
	if err != nil {
		return nil, err
	}
	return f.Credentials[key(issuer, clientID)], nil
}

// Save writes one credential, leaving the others alone.
func (s *Store) Save(c *Credential) error {
	f, err := s.load()
	if err != nil {
		return err
	}
	if f.Credentials == nil {
		f.Credentials = map[string]*Credential{}
	}
	f.Credentials[key(c.Issuer, c.ClientID)] = c
	return s.write(f)
}

// Delete removes one credential. Reports whether anything was there.
func (s *Store) Delete(issuer, clientID string) (bool, error) {
	f, err := s.load()
	if err != nil {
		return false, err
	}
	k := key(issuer, clientID)
	if _, present := f.Credentials[k]; !present {
		return false, nil
	}
	delete(f.Credentials, k)
	if len(f.Credentials) == 0 {
		// Nothing left: remove the file rather than leaving an empty husk,
		// so `lt auth status` and a fresh machine look the same.
		if err := os.Remove(s.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, fmt.Errorf("removing %s: %w", s.path, err)
		}
		return true, nil
	}
	return true, s.write(f)
}

// All returns every cached credential, for `lt auth status`.
func (s *Store) All() ([]*Credential, error) {
	f, err := s.load()
	if err != nil {
		return nil, err
	}
	out := make([]*Credential, 0, len(f.Credentials))
	for _, c := range f.Credentials {
		out = append(out, c)
	}
	return out, nil
}

func (s *Store) load() (*file, error) {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) && s.legacyPath != "" {
		// Nothing at the current (os.UserConfigDir) path yet — try where a
		// version before this one would have written instead of reporting a
		// fresh install's "not signed in" over a perfectly good session.
		raw, err = os.ReadFile(s.legacyPath)
	}
	if errors.Is(err, os.ErrNotExist) {
		return &file{Version: currentVersion, Credentials: map[string]*Credential{}}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", s.path, err)
	}

	// A BOM here is always a tool's doing, never lt's own write — encoding/
	// json writes plain UTF-8 with none. Notepad and `Set-Content -Encoding
	// utf8` under PowerShell 5.1 both add one on save, and json.Unmarshal
	// rejects the leading bytes outright: every branch below would read a
	// perfectly valid file as corrupt. frontmatter.Split strips the same
	// three bytes for the same reason (WINDOWS.md #8); this keeps the two
	// consistent rather than having decided the question twice.
	raw = bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf"))

	var f file
	if err := json.Unmarshal(raw, &f); err == nil && f.Version >= 2 && f.Credentials != nil {
		return &f, nil
	}

	// Fall back to the v1 flat shape.
	var legacy Credential
	if err := json.Unmarshal(raw, &legacy); err == nil && legacy.Issuer != "" && legacy.ClientID != "" {
		return &file{
			Version:     currentVersion,
			Credentials: map[string]*Credential{key(legacy.Issuer, legacy.ClientID): &legacy},
		}, nil
	}

	// Corrupt or unrecognised. A broken cache must send the user to
	// `lt auth login`, not break the CLI — so start empty rather than error.
	return &file{Version: currentVersion, Credentials: map[string]*Credential{}}, nil
}

func (s *Store) write(f *file) error {
	f.Version = currentVersion

	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(s.path), err)
	}

	encoded, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding credentials: %w", err)
	}

	// Written to a temp file in the same directory and renamed over the
	// target, rather than truncated in place, so a crash, a Ctrl-C or a full
	// disk between the two never leaves credentials.json half-written.
	// json.Unmarshal on a truncated file fails, and `load` deliberately
	// treats an unparseable file as empty — so the old failure mode was not
	// an error, it was a silent, confusing "not signed in" with the refresh
	// token gone (WINDOWS.md #7). Same directory matters: os.Rename is only
	// atomic within one filesystem, and the same temp file across every
	// attempt means a crash mid-write leaves at most one stale .tmp behind
	// rather than a new one per failed run.
	//
	// 0600 is applied at *creation*, before a byte of the refresh token is
	// written. Writing first and chmod-ing after leaves the token
	// world-readable for the width of the write, which on a shared machine is
	// long enough. On Windows the perm argument is a no-op; %AppData% is
	// user-scoped by default, and that difference is documented in the README
	// rather than papered over.
	tmp := s.path + ".tmp"
	fh, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("opening %s: %w", tmp, err)
	}
	if _, err := fh.Write(encoded); err != nil {
		fh.Close()
		return fmt.Errorf("writing %s: %w", tmp, err)
	}
	if err := fh.Close(); err != nil {
		return fmt.Errorf("writing %s: %w", tmp, err)
	}
	// Belt and braces: OpenFile's mode is reduced by umask on POSIX, so an
	// unusually permissive umask could otherwise leave the temp file (briefly,
	// but before the rename) more open than intended.
	if err := os.Chmod(tmp, 0o600); err != nil {
		return fmt.Errorf("securing %s: %w", tmp, err)
	}
	// Atomic on POSIX; on Windows, os/file.go's Rename uses MoveFileEx with
	// MOVEFILE_REPLACE_EXISTING, which is the same property.
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("replacing %s: %w", s.path, err)
	}
	return nil
}
