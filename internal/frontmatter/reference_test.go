package frontmatter

import (
	"reflect"
	"testing"
)

// The reference is generated from the structs, so it cannot omit a key the
// code reads. This keeps the words in step: a new field with no description
// fails here, and so do words left behind for a field that is gone.
func TestEveryFieldIsDescribed(t *testing.T) {
	seen := map[string]bool{}
	for _, schema := range Schemas() {
		seen[schema.Name] = true
		if schema.Title == "" || schema.Intro == "" {
			t.Errorf("%s has no title or intro", schema.Name)
		}
		keys := map[string]bool{}
		for _, f := range schema.Fields {
			keys[f.Key] = true
			if f.Description == "" {
				t.Errorf("%s.%s has no description", schema.Name, f.Key)
			}
			if f.Type == "" || f.Type[0] == '[' || f.Type[0] == '*' {
				t.Errorf("%s.%s has no readable type: %q", schema.Name, f.Key, f.Type)
			}
		}
		for key := range descriptions[schema.Name] {
			if !keys[key] {
				t.Errorf("%s describes %q, which is not one of its fields", schema.Name, key)
			}
		}
	}
	for name := range descriptions {
		if !seen[name] {
			t.Errorf("descriptions for %s, which is not a schema", name)
		}
	}
}

// Every struct Split is called with is in the reference.
func TestEveryManifestIsAReferenceSchema(t *testing.T) {
	names := map[string]bool{}
	for _, schema := range Schemas() {
		names[schema.Name] = true
	}
	for _, v := range []any{Meta{}, CourseMeta{}, BookMeta{}, ChapterMeta{}} {
		if name := reflect.TypeOf(v).Name(); !names[name] {
			t.Errorf("%s is read by lt but missing from the frontmatter reference", name)
		}
	}
}
