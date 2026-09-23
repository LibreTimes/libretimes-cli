package frontmatter

import (
	"reflect"
	"testing"
)

// The whole existing corpus writes `tags: a, b, c` because the Python parser
// was deliberately not YAML. Both forms have to produce the same result, or
// this rewrite silently drops metadata from every file already written.
func TestListAcceptsBothForms(t *testing.T) {
	commaSeparated := `---
title: T
type: reference
tags: интегралы, логарифм, справочник
category_ids: math, analysis
---
body`

	yamlSequence := `---
title: T
type: reference
tags:
  - интегралы
  - логарифм
  - справочник
category_ids: [math, analysis]
---
body`

	a, _, err := Split[Meta](commaSeparated)
	if err != nil {
		t.Fatalf("comma form: %v", err)
	}
	b, _, err := Split[Meta](yamlSequence)
	if err != nil {
		t.Fatalf("sequence form: %v", err)
	}

	want := StringList{"интегралы", "логарифм", "справочник"}
	if !reflect.DeepEqual(a.Tags, want) {
		t.Errorf("comma tags = %#v, want %#v", a.Tags, want)
	}
	if !reflect.DeepEqual(a.Tags, b.Tags) {
		t.Errorf("forms disagree:\n comma: %#v\n yaml:  %#v", a.Tags, b.Tags)
	}
	if !reflect.DeepEqual(a.CategoryIDs, b.CategoryIDs) {
		t.Errorf("category_ids disagree:\n comma: %#v\n yaml: %#v", a.CategoryIDs, b.CategoryIDs)
	}
}

func TestListTrimsAndDropsEmpties(t *testing.T) {
	meta, _, err := Split[Meta]("---\ntags:  a ,, b ,  \n---\nx")
	if err != nil {
		t.Fatal(err)
	}
	if want := (StringList{"a", "b"}); !reflect.DeepEqual(meta.Tags, want) {
		t.Errorf("tags = %#v, want %#v", meta.Tags, want)
	}
}

func TestParsesTheDocumentedManifest(t *testing.T) {
	source := `---
title: Интегралы, содержащие логарифм
description: Справочная таблица неопределённых интегралов с логарифмом.
import_key: libretimes-content/reference/ru/integrals-logarithmic
type: reference
language: ru
license: cc_by_sa
visibility: public
ai_usage: assisted
is_draft: true
---
# Heading

Body text.`

	meta, body, err := Split[Meta](source)
	if err != nil {
		t.Fatalf("Split: %v", err)
	}

	if meta.Title != "Интегралы, содержащие логарифм" {
		t.Errorf("title = %q", meta.Title)
	}
	if meta.ImportKey != "libretimes-content/reference/ru/integrals-logarithmic" {
		t.Errorf("import_key = %q", meta.ImportKey)
	}
	if meta.Type != "reference" || meta.Language != "ru" || meta.License != "cc_by_sa" {
		t.Errorf("meta = %+v", meta)
	}
	if meta.IsDraft == nil || !*meta.IsDraft {
		t.Errorf("is_draft = %v, want true", meta.IsDraft)
	}
	if body != "# Heading\n\nBody text." {
		t.Errorf("body = %q", body)
	}
}

// is_draft absent must be distinguishable from is_draft: false, or a PATCH
// would publish a draft the file never mentioned.
func TestIsDraftAbsentIsNil(t *testing.T) {
	meta, _, _ := Split[Meta]("---\ntitle: T\n---\nbody")
	if meta.IsDraft != nil {
		t.Errorf("absent is_draft = %v, want nil", *meta.IsDraft)
	}

	meta, _, _ = Split[Meta]("---\nis_draft: false\n---\nbody")
	if meta.IsDraft == nil || *meta.IsDraft {
		t.Errorf("explicit false should decode to a pointer to false, got %v", meta.IsDraft)
	}
}

// Silently eating a whole document because someone opened with `---` as a
// horizontal rule would be far worse than ignoring metadata that isn't there.
func TestMalformedBlocksAreTreatedAsContent(t *testing.T) {
	cases := []struct{ name, source string }{
		{"no frontmatter", "# Just a heading\n\nText."},
		{"never closed", "---\ntitle: T\n\nbody with no closing delimiter"},
		{"horizontal rule first", "---\n\nA thematic break, not frontmatter."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			meta, body, err := Split[Meta](tc.source)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if meta.Title != "" {
				t.Errorf("parsed a title out of non-frontmatter: %q", meta.Title)
			}
			if body != tc.source {
				t.Errorf("content was altered:\n got: %q\nwant: %q", body, tc.source)
			}
		})
	}
}

func TestUnknownKeysAreTolerated(t *testing.T) {
	// A file may carry frontmatter for other tools; failing the whole import
	// over a key lt does not use would be rude.
	meta, _, err := Split[Meta]("---\ntitle: T\nweight: 4\ndraft_note: whatever\n---\nbody")
	if err != nil {
		t.Fatalf("unknown keys should not error: %v", err)
	}
	if meta.Title != "T" {
		t.Errorf("title = %q", meta.Title)
	}
}

func TestInvalidYAMLIsAnError(t *testing.T) {
	if _, _, err := Split[Meta]("---\ntitle: [unclosed\n---\nbody"); err == nil {
		t.Error("malformed YAML inside a well-formed block should error")
	}
}

func TestBOMIsTolerated(t *testing.T) {
	meta, _, err := Split[Meta]("\ufeff---\ntitle: T\n---\nbody")
	if err != nil {
		t.Fatal(err)
	}
	if meta.Title != "T" {
		t.Errorf("a leading BOM stopped the frontmatter parsing: %+v", meta)
	}
}
