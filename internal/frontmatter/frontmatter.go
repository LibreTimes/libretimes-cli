// Package frontmatter reads the YAML block at the head of a markdown file.
//
// The frontmatter *is* the import manifest — there is no second file mapping
// articles to fields.
package frontmatter

import (
	"bufio"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// Meta is what lt reads out of a file. Unknown keys are tolerated rather than
// rejected: a file may reasonably carry frontmatter for other tools, and
// failing someone's whole import over a key lt does not use would be rude.
type Meta struct {
	Title       string     `yaml:"title"`
	Description string     `yaml:"description"`
	ImportKey   string     `yaml:"import_key"`
	Type        string     `yaml:"type"`
	Language    string     `yaml:"language"`
	License     string     `yaml:"license"`
	Place       string     `yaml:"place"`
	Visibility  string     `yaml:"visibility"`
	AIUsage     string     `yaml:"ai_usage"`
	IsDraft     *bool      `yaml:"is_draft"`
	Tags        StringList `yaml:"tags"`
	CategoryIDs StringList `yaml:"category_ids"`
	// Contributors are the names credited on the byline besides the signed-in
	// account, which owns the publication and is on it already.
	Contributors []Contributor `yaml:"contributors"`
}

// CourseMeta is a course manifest's frontmatter: the collection that groups one
// lecture course's notes, for `lt course publish`.
//
// Lectures are paths to the lecture files, relative to the manifest, in course
// order. Each is identified by the `import_key` in its own frontmatter -- the
// same key `lt publish` used -- so renaming a lecture's key in its file is the
// only edit a course needs, never a second list of keys to keep in step.
type CourseMeta struct {
	ImportKey   string     `yaml:"import_key"`
	Title       string     `yaml:"title"`
	Description string     `yaml:"description"`
	Language    string     `yaml:"language"`
	Visibility  string     `yaml:"visibility"`
	CategoryIDs StringList `yaml:"category_ids"`
	Lectures    StringList `yaml:"lectures"`
}

// BookMeta is a book manifest's frontmatter, for `lt book publish`: the
// publication's fields, and `contents`, the book's table of contents as a tree.
//
// The manifest is authoritative for order, unlike a course's: a book is one
// text, so a chapter the manifest no longer names is a question the run asks
// rather than an item it leaves alone.
type BookMeta struct {
	ImportKey    string         `yaml:"import_key"`
	Type         string         `yaml:"type"`
	Title        string         `yaml:"title"`
	Description  string         `yaml:"description"`
	Language     string         `yaml:"language"`
	License      string         `yaml:"license"`
	Visibility   string         `yaml:"visibility"`
	AIUsage      string         `yaml:"ai_usage"`
	IsDraft      *bool          `yaml:"is_draft"`
	Tags         StringList     `yaml:"tags"`
	CategoryIDs  StringList     `yaml:"category_ids"`
	Contributors []Contributor  `yaml:"contributors"`
	Contents     []ContentEntry `yaml:"contents"`
}

// ContentEntry is one node of a book's contents. File, when set, is the
// chapter's text, relative to the manifest; a node without one is navigation
// (a part holding chapters). Label, Title, Ref, Kind and FirstLine may be
// written here or in the chapter file's own frontmatter, not both differently.
type ContentEntry struct {
	File      string         `yaml:"file"`
	Kind      string         `yaml:"kind"`
	Label     string         `yaml:"label"`
	Title     string         `yaml:"title"`
	Ref       string         `yaml:"ref"`
	FirstLine string         `yaml:"first_line"`
	Children  []ContentEntry `yaml:"children"`
}

// ChapterMeta is the optional frontmatter of one chapter file.
type ChapterMeta struct {
	Kind      string `yaml:"kind"`
	Label     string `yaml:"label"`
	Title     string `yaml:"title"`
	Ref       string `yaml:"ref"`
	FirstLine string `yaml:"first_line"`
}

// Contributor is one byline credit as a file writes it.
//
// Profile is `"@handle"` (quoted: a bare `@` cannot start a YAML scalar), a
// bare handle, or a profile_id. Role is a ContributorRole and defaults to
// `author`. There is no `access` key: a credit from a file is always
// `credited`, never edit rights.
type Contributor struct {
	Profile string `yaml:"profile"`
	Role    string `yaml:"role"`
}

// StringList accepts either a real YAML sequence or a comma-separated scalar.
//
// The Python importer this replaces was deliberately not a YAML parser — flat
// scalars only, to avoid carrying a parser for untrusted input to read six
// lines — so every existing file writes `tags: a, b, c` rather than a
// sequence. Supporting both means the whole existing corpus keeps working
// unchanged while new files can be written the natural way.
type StringList []string

func (s *StringList) UnmarshalYAML(node *yaml.Node) error {
	switch node.Kind {
	case yaml.SequenceNode:
		var items []string
		if err := node.Decode(&items); err != nil {
			return err
		}
		*s = trimAll(items)
		return nil
	case yaml.ScalarNode:
		var raw string
		if err := node.Decode(&raw); err != nil {
			return err
		}
		*s = trimAll(strings.Split(raw, ","))
		return nil
	default:
		return fmt.Errorf("expected a list or a comma-separated string, got %v", node.Kind)
	}
}

func trimAll(items []string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Split separates the frontmatter block from the body, decoding it into M.
// Generic because the block-finding logic below is entirely product-agnostic;
// only the fields being decoded differ. M is not inferable from the arguments,
// so callers always spell it: `Split[Meta](text)`.
//
// A file with no opening delimiter, or with an opening delimiter and no
// closing one, is treated as entirely content — silently eating a whole
// document because someone typed `---` as a horizontal rule on line one would
// be much worse than ignoring metadata that was never there.
func Split[M any](text string) (M, string, error) {
	var meta M

	// Tolerate a UTF-8 BOM; editors on Windows add one and it would otherwise
	// stop the delimiter matching. Spelled as an escape, not the literal
	// character — a raw BOM mid-source is a compile error.
	text = strings.TrimPrefix(text, "\ufeff")

	if !strings.HasPrefix(text, "---") {
		return meta, text, nil
	}

	scanner := bufio.NewScanner(strings.NewReader(text))
	// Frontmatter lines are short, but a stray long line should not error the
	// scanner out. 1 MiB is far more than any manifest needs.
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)

	var (
		first   = true
		inBlock = false
		block   []string
		body    []string
	)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case first:
			first = false
			if strings.TrimSpace(line) != "---" {
				return meta, text, nil
			}
			inBlock = true
		case inBlock && strings.TrimSpace(line) == "---":
			inBlock = false
		case inBlock:
			block = append(block, line)
		default:
			body = append(body, line)
		}
	}
	if err := scanner.Err(); err != nil {
		return meta, "", fmt.Errorf("reading the file: %w", err)
	}

	if inBlock {
		// Opened and never closed. Treat the whole thing as content.
		return meta, text, nil
	}

	if err := yaml.Unmarshal([]byte(strings.Join(block, "\n")), &meta); err != nil {
		return meta, "", fmt.Errorf("the frontmatter is not valid YAML: %w", err)
	}

	return meta, strings.TrimLeft(strings.Join(body, "\n"), "\n"), nil
}
