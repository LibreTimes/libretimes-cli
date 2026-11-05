package frontmatter

import (
	"fmt"
	"reflect"
	"strings"
)

// The frontmatter reference (`lt help frontmatter`, and the manual's page of
// the same name) is generated from the structs in this package, so a key the
// code reads is a key the manual names. The words live here, beside the
// declarations; TestEveryFieldIsDescribed fails on a field with none, and on
// words left behind for a field that is gone.

// Field is one frontmatter key as the manual shows it.
type Field struct {
	Key         string
	Type        string
	Description string
}

// Schema is one kind of file and the keys it may carry.
type Schema struct {
	Name   string // the struct, for tests
	Title  string
	Intro  string
	Fields []Field
}

type schemaSource struct {
	value any
	title string
	intro string
}

var schemaSources = []schemaSource{
	{Meta{}, "A work file (`lt publish`)",
		"One markdown file is one work: the frontmatter describes it and the body below is its text."},
	{CourseMeta{}, "A course manifest (`lt course publish`)",
		"Names a course and lists its lecture files in order. The body below the frontmatter is never sent."},
	{BookMeta{}, "A book manifest (`lt book publish`)",
		"Names a book and lists its contents as a tree. The body below the frontmatter is never sent."},
	{ContentEntry{}, "An entry of a book's `contents`",
		"One node of the tree: a chapter with a file, or a heading that holds `children`."},
	{ChapterMeta{}, "A chapter file's frontmatter (optional)",
		"The same per-chapter keys as a `contents` entry. Write each in the manifest or in the file, not both differently."},
	{Contributor{}, "An entry of `contributors`",
		"One name credited on the byline besides the signed-in account, which owns the work and is on it already."},
}

// descriptions are the words for each struct's keys.
var descriptions = map[string]map[string]string{
	"Meta": {
		"title":        "The work's title.",
		"description":  "The short text shown above the body: a description or, for a scientific work, its abstract.",
		"import_key":   "What makes a re-run an update rather than a duplicate. Defaults to `lt:<directory>/<file stem>`; set it, because a key derived from a path breaks when the file moves.",
		"type":         "Required, no default. The kind of work: one of the kinds `lt types` lists, read from the platform's registry.",
		"language":     "The language the text is written in, as a code (`en`, `ru`).",
		"license":      "The license the work is published under, as its key (`cc_by_sa`).",
		"place":        "Where the work was given or written, as free text (up to 100 characters).",
		"visibility":   "Who may read it.",
		"ai_usage":     "How much of the text an AI wrote.",
		"is_draft":     "`true` keeps the work a draft. On a create, leaving it out publishes; on an update, leaving it out keeps the current state. Books and scientific works must say `true`: a venue publishes them.",
		"tags":         "Up to 10 tags.",
		"category_ids": "Up to 25 category ids. UUIDs, not category keys.",
		"contributors": "Names to credit on the byline. Only ever added: removing one from the file removes nothing.",
	},
	"CourseMeta": {
		"import_key":   "Required. Identifies the course on re-runs.",
		"title":        "Required. The course's title.",
		"description":  "The course's description.",
		"language":     "Required. The language the title and description are written in, as a code.",
		"visibility":   "Who may see the course.",
		"category_ids": "Category ids, as for a work.",
		"lectures":     "The lecture files, relative to the manifest, in course order. Each is found by the `import_key` in its own frontmatter.",
	},
	"BookMeta": {
		"import_key":   "Required. Identifies the book on re-runs.",
		"type":         "Leave it out, or write `book`: a book manifest is always a book.",
		"title":        "Required. The book's title.",
		"description":  "The book's description.",
		"language":     "Required. Which of the book's texts the chapters are written to.",
		"license":      "As for a work.",
		"visibility":   "As for a work.",
		"ai_usage":     "As for a work.",
		"is_draft":     "`true` keeps the book a draft, which a venue-published kind requires. On a create, leaving it out publishes once the chapters are written.",
		"tags":         "As for a work.",
		"category_ids": "As for a work.",
		"contributors": "As for a work: names to credit, only ever added.",
		"contents":     "Required. The book's contents as a tree, in reading order. The manifest is authoritative for order.",
	},
	"ContentEntry": {
		"file":       "The chapter's text, relative to the manifest. Without one, the entry is a heading in the contents, not a page.",
		"kind":       "What the division is. Defaults to `chapter`, or to `part` for an entry with children and no file.",
		"label":      "The division's own label (`1`, `Preface`).",
		"title":      "The division's title.",
		"ref":        "The book's own numbering (`I.1`, `Prop. 12`). A chapter with one is matched to the site by it, so a reorder moves it, text and all; without one, by position.",
		"first_line": "The first line, for a division known by it (a poem).",
		"children":   "The entries under this one.",
	},
	"ChapterMeta": {
		"kind":       "As in `contents`.",
		"label":      "As in `contents`.",
		"title":      "As in `contents`.",
		"ref":        "As in `contents`.",
		"first_line": "As in `contents`.",
	},
	"Contributor": {
		"profile": "Who: `\"@handle\"` (quoted, since a bare `@` cannot start a YAML value), a bare handle, or a profile_id.",
		"role":    "How they contributed. Defaults to `author`.",
	},
}

// Schemas is every file kind lt reads, with its keys in declaration order.
func Schemas() []Schema {
	out := make([]Schema, 0, len(schemaSources))
	for _, src := range schemaSources {
		t := reflect.TypeOf(src.value)
		words := descriptions[t.Name()]
		schema := Schema{Name: t.Name(), Title: src.title, Intro: src.intro}
		for i := range t.NumField() {
			f := t.Field(i)
			key := yamlKey(f)
			if key == "" {
				continue
			}
			schema.Fields = append(schema.Fields, Field{
				Key: key, Type: typeName(f.Type), Description: words[key],
			})
		}
		out = append(out, schema)
	}
	return out
}

func yamlKey(f reflect.StructField) string {
	tag := f.Tag.Get("yaml")
	key, _, _ := strings.Cut(tag, ",")
	if key == "-" || !f.IsExported() {
		return ""
	}
	return key
}

func typeName(t reflect.Type) string {
	switch {
	case t == reflect.TypeOf(StringList{}):
		return "list, or comma-separated text"
	case t.Kind() == reflect.Pointer && t.Elem().Kind() == reflect.Bool:
		return "true or false"
	case t.Kind() == reflect.String:
		return "text"
	case t == reflect.TypeOf([]Contributor{}):
		return "list of contributors"
	case t == reflect.TypeOf([]ContentEntry{}):
		return "list of contents entries"
	default:
		return fmt.Sprint(t)
	}
}
