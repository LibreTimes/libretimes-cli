package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/The-LibreTimes/libretimes-cli/internal/frontmatter"
)

// A collection manifest is a plain YAML or JSON file: the collection's fields,
// its items and its sections, nothing else. Items are named by identifier or
// key (a DOI, an ISBN, `book:<key>`), so one file means the same list on every
// deployment.
//
// The one exception is a course, whose manifest may stay the README of its
// folder, the frontmatter being the manifest. A README for any other kind is
// refused: a list is one file of data, and a second form of it is a second
// thing to keep in step.

// isMarkdownManifest says whether a manifest path is the README form.
func isMarkdownManifest(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".md" || ext == ".markdown"
}

// loadCollectionManifest reads a manifest in either form. A plain file is
// strict: a key lt does not know is an error, because in a file that is only
// a manifest it is a typo. A README's frontmatter tolerates other tools' keys,
// as a work's does.
func loadCollectionManifest(path string) (frontmatter.CollectionMeta, error) {
	var meta frontmatter.CollectionMeta
	raw, err := os.ReadFile(path)
	if err != nil {
		return meta, err
	}
	if isMarkdownManifest(path) {
		// The body is the course folder's own notes (a lecture table, its
		// status), never sent: the description is the frontmatter's.
		meta, _, err := frontmatter.Split[frontmatter.CollectionMeta](string(raw))
		return meta, err
	}

	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		// Through YAML, so both forms share one decoder and its item rules.
		var generic any
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&generic); err != nil {
			return meta, fmt.Errorf("not valid JSON: %w", err)
		}
		if raw, err = yaml.Marshal(generic); err != nil {
			return meta, err
		}
	case ".yaml", ".yml":
	default:
		return meta, fmt.Errorf("a collection manifest is a .yaml, .yml or .json file " +
			"(or a course's README.md)")
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&meta); err != nil {
		return meta, fmt.Errorf("reading the manifest: %w", err)
	}
	return meta, nil
}
