package cli

import (
	"path/filepath"
	"testing"
)

// resolveFiles is shared by every publish-style command; these pin the glob
// behaviour WINDOWS.md #1 asked for, independent of which command calls it.

func TestResolveFilesPassesLiteralPathsThrough(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "doc.md", "x")

	got, err := resolveFiles([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != path {
		t.Errorf("got %v, want [%s]", got, path)
	}
}

// A real file named with glob metacharacters must still resolve as itself,
// not be treated as a pattern to expand.
func TestResolveFilesLiteralPathWinsOverGlobMeta(t *testing.T) {
	dir := t.TempDir()
	path := writeFile(t, dir, "[draft].md", "x")

	got, err := resolveFiles([]string{path})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != path {
		t.Errorf("got %v, want [%s]", got, path)
	}
}

func TestResolveFilesExpandsAGlob(t *testing.T) {
	dir := t.TempDir()
	a := writeFile(t, dir, "a.md", "x")
	b := writeFile(t, dir, "b.md", "x")
	writeFile(t, dir, "c.txt", "x") // must not match *.md

	got, err := resolveFiles([]string{filepath.Join(dir, "*.md")})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %v, want 2 matches", got)
	}
	want := map[string]bool{a: true, b: true}
	for _, g := range got {
		if !want[g] {
			t.Errorf("unexpected match %s", g)
		}
	}
}

func TestResolveFilesGlobWithNoMatchesFails(t *testing.T) {
	dir := t.TempDir()
	_, err := resolveFiles([]string{filepath.Join(dir, "*.md")})
	if err == nil {
		t.Fatal("expected an error for a glob matching nothing")
	}
}

func TestResolveFilesMissingLiteralPathFails(t *testing.T) {
	_, err := resolveFiles([]string{filepath.Join(t.TempDir(), "nope.md")})
	if err == nil {
		t.Fatal("expected an error for a missing literal path")
	}
}

func TestResolveFilesRefusesDoubleStarRatherThanSilentlyNarrowing(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "nested/doc.md", "x")

	_, err := resolveFiles([]string{filepath.Join(dir, "**", "*.md")})
	if err == nil {
		t.Fatal("expected ** to be refused rather than silently matching one level")
	}
}

func TestResolveFilesExplicitDirectoryStillErrors(t *testing.T) {
	dir := t.TempDir()
	_, err := resolveFiles([]string{dir})
	if err == nil {
		t.Fatal("expected an explicit directory argument to be refused")
	}
}

// A directory caught by a glob (content/* matching a subdirectory alongside
// its files) is skipped rather than refused -- nobody typed that path on
// purpose the way an explicit directory argument is.
func TestResolveFilesSkipsADirectoryAmongGlobMatches(t *testing.T) {
	dir := t.TempDir()
	doc := writeFile(t, dir, "doc.md", "x")
	writeFile(t, dir, "subdir/nested.md", "x") // creates dir/subdir

	got, err := resolveFiles([]string{filepath.Join(dir, "*")})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != doc {
		t.Errorf("got %v, want only [%s] (subdir skipped)", got, doc)
	}
}
