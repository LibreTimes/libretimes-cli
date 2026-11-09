package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/The-LibreTimes/libretimes-cli/internal/api"
	"github.com/The-LibreTimes/libretimes-cli/internal/config"
	"github.com/The-LibreTimes/libretimes-cli/internal/frontmatter"
)

// What one listed work of a collection resolves to. Additive only, like byline
// credits: `lt collection publish` never removes an item from a collection.
const (
	itemAdd     = "add"
	itemPresent = "present"
	itemUpdate  = "update" // present, and its note, edition or section is set to the manifest's
	itemSkip    = "skip"
)

// Why a listed work was skipped. None of them fails the collection: works are
// published one at a time, so a manifest naming works that are not on the site
// yet is the normal state, not an error.
const (
	skipNotPublished = "not_published" // no publication holds this key yet
	skipDeleted      = "deleted"       // a deleted publication holds it
	skipDraft        = "draft"         // a draft holds it; readers could not open it
	skipNotFound     = "not_found"     // no work with that id is visible to you
	skipNotIndexed   = "not_indexed"   // the DOI / arXiv id / ISBN names no work the site holds
)

// itemResult is one listed work's outcome, reported under its collection.
// PublicationID is the work's id, kept under its old name for callers that
// already read it.
type itemResult struct {
	Path          string   `json:"path"`
	ImportKey     string   `json:"import_key,omitempty"`
	Action        string   `json:"action"`
	Reason        string   `json:"reason,omitempty"`
	PublicationID string   `json:"publication_id,omitempty"`
	Title         string   `json:"title,omitempty"`
	Section       string   `json:"section,omitempty"`
	Edition       string   `json:"edition,omitempty"`
	Changes       []string `json:"changes,omitempty"`

	// What the manifest asks for, not part of the report.
	note    string
	pin     string
	section int // index into the manifest's sections; -1 is the preamble
}

// sectionResult is one manifest section's outcome.
type sectionResult struct {
	Title     string `json:"title"`
	Action    string `json:"action"` // create, present or update
	SectionID string `json:"section_id,omitempty"`
}

// collectionResult is one manifest's outcome, and the shape --json emits.
type collectionResult struct {
	Path           string          `json:"path"`
	ImportKey      string          `json:"import_key"`
	CollectionType string          `json:"collection_type,omitempty"`
	Action         string          `json:"action"`
	CollectionID   string          `json:"collection_id,omitempty"`
	Code           string          `json:"code,omitempty"`
	Message        string          `json:"message,omitempty"`
	Sections       []sectionResult `json:"sections,omitempty"`
	Items          []itemResult    `json:"items,omitempty"`
	// Reordered is true when the collection's order did not match the
	// manifest's, and the run puts it back.
	Reordered bool `json:"reordered,omitempty"`
	// Unlisted counts items in the collection that the manifest does not name.
	// They are left where they are.
	Unlisted int `json:"unlisted,omitempty"`
}

func (r *collectionResult) setFailure(err error) {
	r.Action = actionFailed
	r.Code, r.Message = codeAndMessage(err)
}

func (r *collectionResult) invalid(format string, args ...any) {
	r.Action = actionFailed
	r.Code = "validation_error"
	r.Message = fmt.Sprintf(format, args...)
}

// `lt collection` is the curated lists: a course, a plain collection, a
// recommendation list. The kind is the manifest's `collection_type`, not a
// command of its own, for the reason a book is a `type` of work.

func newCollectionCommand(e *env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "collection",
		Short: "Publish and manage your collections: courses and curated lists",
	}
	cmd.AddCommand(
		newCollectionPublishCommand(e),
		newCollectionLsCommand(e),
		newCollectionShowCommand(e),
		newCollectionExportCommand(e),
		newTranslationsCommand(e, collectionTranslations),
	)
	return cmd
}

func newCollectionPublishCommand(e *env) *cobra.Command {
	var dryRun, yes bool

	cmd := &cobra.Command{
		Use:   "publish <manifest.md>...",
		Short: "Reconcile collection manifests as collections",
		Long: "Reconcile one or more collection manifests as LibreTimes collections.\n\n" +
			"The manifest's frontmatter names the collection (`import_key`, " +
			"`collection_type`, `title`, `language`) and lists the files of its works in " +
			"order under `items` and, grouped under headings, `sections` (title, description, " +
			"items). An item is a work file, a work id or URL, a DOI, an arXiv id or an ISBN, " +
			"or a mapping {work, note, edition}. Works not on the site yet, or still drafts, " +
			"are skipped and picked up by the next run. Re-runs find sections by title, add " +
			"what is missing, set notes and edition pins, and put the order back; nothing is " +
			"ever removed. Run with --dry-run first.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, err := resolveFiles(args)
			if err != nil {
				return err
			}
			if err := needsConfirm(dryRun, yes,
				fmt.Sprintf("collection publish would write %d collection(s)", len(paths))); err != nil {
				return err
			}

			// Required even for --dry-run, as for `lt work publish`: the plan
			// is read off your own collections and works.
			client, _, err := e.client(cmd.Context(), true, config.ProductLibreTimes)
			if err != nil {
				return err
			}

			results := make([]collectionResult, 0, len(paths))
			failed := false
			for _, path := range paths {
				result := publishCollection(cmd.Context(), client, path, dryRun)
				results = append(results, result)
				if result.Action == actionFailed {
					failed = true
				}
				if !e.jsonOut {
					renderCollection(e, result)
				}
			}

			if e.jsonOut {
				if err := e.emit(results, func(io.Writer) {}); err != nil {
					return err
				}
			}
			if failed {
				return &exitError{code: ExitAPIError, err: errQuiet{}}
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show the plan; write nothing.")
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm the writes.")
	return cmd
}

func renderCollection(e *env, r collectionResult) {
	if r.Action == actionFailed {
		fmt.Fprintf(e.err, "  %-8s %-40s %s: %s\n", r.Action, r.ImportKey, r.Code, r.Message)
		if r.CollectionID == "" {
			return
		}
	} else {
		if e.quiet {
			return
		}
		fmt.Fprintf(e.out, "  %-8s %-40s %s\n", r.Action, r.ImportKey, r.CollectionID)
	}
	for _, sec := range r.Sections {
		fmt.Fprintf(e.out, "    section  %-8s %s\n", sec.Action, sec.Title)
	}
	for _, l := range r.Items {
		trailing := l.PublicationID
		if l.Action == itemSkip {
			trailing = l.Reason
		} else if len(l.Changes) > 0 {
			trailing += " (" + strings.Join(l.Changes, ", ") + ")"
		}
		label := l.ImportKey
		if label == "" {
			label = l.Path
		}
		fmt.Fprintf(e.out, "    %-8s %-38s %s\n", l.Action, label, trailing)
	}
	if r.Reordered {
		fmt.Fprintln(e.out, "    reorder  items follow the manifest's order")
	}
	if r.Unlisted > 0 {
		fmt.Fprintf(e.out, "    %d item(s) in the collection are not in the manifest; left in place\n", r.Unlisted)
	}
}

func newCollectionLsCommand(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List your collections",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, _, err := e.client(cmd.Context(), true, config.ProductLibreTimes)
			if err != nil {
				return err
			}
			colls, err := client.ListMyCollections(cmd.Context())
			if err != nil {
				return err
			}
			return e.emit(colls, func(w io.Writer) {
				if len(colls) == 0 {
					fmt.Fprintln(w, "  no collections yet")
				}
				for _, c := range colls {
					fmt.Fprintf(w, "  %-36s %-14s %-13s %3d  %s\n", c.ID, c.CollectionType, c.Visibility,
						c.ItemsCount, c.Title)
				}
			})
		},
	}
}

// collectionShow is what `lt collection show --json` emits.
type collectionShow struct {
	api.CollectionSummary
	Items        []api.CollectionItem      `json:"items"`
	Translations *api.TranslationsOverview `json:"translations,omitempty"`
}

func newCollectionShowCommand(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "show <collection>",
		Short: "Show one collection: its kind, languages and items",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := e.client(cmd.Context(), true, config.ProductLibreTimes)
			if err != nil {
				return err
			}
			id, err := resolveCollection(cmd.Context(), client, args[0])
			if err != nil {
				return err
			}
			coll, err := client.GetCollection(cmd.Context(), id)
			if err != nil {
				return err
			}
			items, err := client.ListCollectionItems(cmd.Context(), id)
			if err != nil {
				return err
			}
			out := collectionShow{CollectionSummary: *coll, Items: items}
			if overview, err := client.GetTranslations(cmd.Context(), api.TranslateCollection, id); err == nil {
				out.Translations = overview
			}
			return e.emit(out, func(w io.Writer) {
				fmt.Fprintf(w, "%s\n", coll.Title)
				fmt.Fprintf(w, "  id          %s\n", coll.ID)
				fmt.Fprintf(w, "  type        %s\n", coll.CollectionType)
				fmt.Fprintf(w, "  visibility  %s\n", coll.Visibility)
				fmt.Fprintf(w, "  language    %s\n", coll.PrimaryLanguage)
				if out.Translations != nil {
					var langs []string
					for _, l := range out.Translations.Languages {
						if l.Language != out.Translations.PrimaryLanguage {
							langs = append(langs, l.Language)
						}
					}
					if len(langs) > 0 {
						fmt.Fprintf(w, "  translated  %s\n", strings.Join(langs, ", "))
					}
				}
				titles := map[string]string{}
				for _, sec := range coll.Sections {
					titles[strings.ToLower(sec.ID)] = sec.Title
				}
				for i, item := range items {
					line := fmt.Sprintf("  %3d. %s %s", i+1, item.EntityType, item.EntityID)
					if title := titles[strings.ToLower(item.SectionID)]; title != "" {
						line += "  [" + title + "]"
					}
					if item.ExpressionID != "" {
						line += "  edition " + item.ExpressionID
					}
					fmt.Fprintln(w, line)
					if item.Note != "" {
						fmt.Fprintf(w, "       note: %s\n", item.Note)
					}
				}
			})
		},
	}
}

// publishCollection resolves and applies one manifest.
//
// Find-then-create, as `lt work publish` does, never create-then-fallback.
// Every listed work resolves before anything is written, so a manifest naming a
// missing file fails whole rather than creating a collection without it.
func publishCollection(ctx context.Context, client *api.Client, path string, dryRun bool) collectionResult {
	result := collectionResult{Path: path}

	raw, err := os.ReadFile(path)
	if err != nil {
		result.setFailure(err)
		return result
	}
	meta, _, err := frontmatter.Split[frontmatter.CollectionMeta](string(raw))
	if err != nil {
		result.setFailure(err)
		return result
	}

	// No default key. A work's key can fall back to its path, but a manifest is
	// a README in the collection's folder, and a key derived from that would
	// name the file rather than the collection.
	result.ImportKey = strings.TrimSpace(meta.ImportKey)
	result.CollectionType = strings.TrimSpace(meta.CollectionType)
	if kind := strings.TrimSpace(meta.Kind); kind != "" {
		if result.CollectionType != "" && result.CollectionType != kind {
			result.invalid("`kind` (%s) and `collection_type` (%s) disagree; write one", kind, result.CollectionType)
			return result
		}
		result.CollectionType = kind
	}
	switch {
	case result.ImportKey == "":
		result.invalid("frontmatter has no `import_key`; a collection needs one, e.g. <author>/<course>")
		return result
	case result.CollectionType == "":
		result.invalid("frontmatter has no `collection_type` (or `kind`), which is required and has no default (one of %s)",
			strings.Join(api.CollectionTypeValues, ", "))
		return result
	case !slices.Contains(api.CollectionTypeValues, result.CollectionType):
		result.invalid("collection_type %q is not one of %s", result.CollectionType,
			strings.Join(api.CollectionTypeValues, ", "))
		return result
	case strings.TrimSpace(meta.Title) == "":
		result.invalid("frontmatter has no `title`")
		return result
	case strings.TrimSpace(meta.Language) == "":
		result.invalid("frontmatter has no `language`, the language the title and description are written in")
		return result
	case meta.Visibility != "" && !slices.Contains(api.CollectionVisibilityValues, meta.Visibility):
		result.invalid("visibility %q is not one of %s (a collection is not a work: "+
			"there is no private or by_link)", meta.Visibility,
			strings.Join(api.CollectionVisibilityValues, ", "))
		return result
	}

	var entries []manifestEntry
	for _, spec := range meta.Items {
		entries = append(entries, manifestEntry{spec, -1})
	}
	seenTitle := map[string]bool{}
	for si := range meta.Sections {
		sec := &meta.Sections[si]
		sec.Title = strings.TrimSpace(sec.Title)
		switch {
		case sec.Title == "":
			result.invalid("section %d has no `title`", si+1)
			return result
		case len(sec.Title) > 255:
			result.invalid("section %q: a title is at most 255 characters", sec.Title)
			return result
		case seenTitle[sec.Title]:
			result.invalid("two sections are titled %q; a re-run finds a section by its title", sec.Title)
			return result
		}
		seenTitle[sec.Title] = true
		for _, spec := range sec.Items {
			entries = append(entries, manifestEntry{spec, si})
		}
	}

	listed, err := resolveItems(ctx, client, path, entries)
	if err != nil {
		result.setFailure(err)
		return result
	}
	result.Items = listed
	for i := range result.Items {
		if sec := result.Items[i].section; sec >= 0 {
			result.Items[i].Section = meta.Sections[sec].Title
		}
		result.Items[i].Edition = result.Items[i].pin
	}

	input := api.CollectionInput{
		Title:           meta.Title,
		Description:     meta.Description,
		PrimaryLanguage: meta.Language,
		Visibility:      meta.Visibility,
		CollectionType:  result.CollectionType,
		CategoryIDs:     meta.CategoryIDs,
		Keywords:        meta.Keywords,
		PublicComments:  meta.PublicComments,
	}

	existing, err := client.FindCollectionByImportKey(ctx, result.ImportKey)
	if err != nil {
		result.setFailure(err)
		return result
	}

	if existing == nil {
		result.Action = actionCreate
		for i := range result.Items {
			if result.Items[i].Action != itemSkip {
				result.Items[i].Action = itemAdd
			}
		}
		for _, sec := range meta.Sections {
			result.Sections = append(result.Sections, sectionResult{Title: sec.Title, Action: actionCreate})
		}
		if dryRun {
			return result
		}
		input.ImportKey = result.ImportKey
		created, err := client.CreateCollection(ctx, input)
		if err != nil {
			result.setFailure(err)
			return result
		}
		if created.ImportKey != result.ImportKey {
			// A server without collection import keys answers the lookup with a
			// plain 404 and ignores the field on create. Carrying on would make
			// a new collection on every run, so the one just made is removed.
			result.Code = "unsupported_server"
			result.Action = actionFailed
			result.Message = "this API does not store a collection's import_key yet, so a re-run " +
				"could not find this collection again; nothing was kept"
			if err := client.DeleteCollection(ctx, created.ID); err != nil {
				result.CollectionID = created.ID
				result.Message = fmt.Sprintf("this API does not store a collection's import_key yet, "+
					"and removing the collection it created failed (%v); delete %s by hand", err, created.ID)
			}
			return result
		}
		result.CollectionID = created.ID
		sectionIDs := make([]string, len(meta.Sections))
		for si, sec := range meta.Sections {
			made, err := client.CreateSection(ctx, created.ID, api.SectionInput{Title: sec.Title, Description: sec.Description})
			if err != nil {
				result.setFailure(fmt.Errorf("created, but adding section %q failed: %w", sec.Title, err))
				return result
			}
			sectionIDs[si] = strings.ToLower(made.ID)
			result.Sections[si].SectionID = sectionIDs[si]
		}
		// Appended in manifest order, preamble first, so positions already
		// follow it and sections were created in their order.
		var adds []api.ItemIn
		for _, r := range result.Items {
			if r.Action == itemSkip {
				continue
			}
			adds = append(adds, itemIn(r, sectionIDs))
		}
		if len(adds) > 0 {
			if err := client.AddWorksToCollection(ctx, created.ID, adds); err != nil {
				result.setFailure(fmt.Errorf("created, but adding items failed: %w", err))
			}
		}
		return result
	}

	result.Action = actionUpdate
	result.CollectionID = existing.ID
	current, err := client.GetCollection(ctx, existing.ID)
	if err != nil {
		result.setFailure(fmt.Errorf("reading the collection: %w", err))
		return result
	}
	items, err := client.ListCollectionItems(ctx, existing.ID)
	if err != nil {
		result.setFailure(fmt.Errorf("reading the collection's items: %w", err))
		return result
	}

	// Sections are found by title. An existing one is reused, a missing one is
	// created, and one the manifest does not name is left alone.
	wantSection := make([]string, len(meta.Sections))
	sectionIDs := make([]string, len(meta.Sections))
	for si, sec := range meta.Sections {
		sr := sectionResult{Title: sec.Title, Action: actionCreate}
		wantSection[si] = "new:" + sec.Title
		for _, have := range current.Sections {
			if strings.TrimSpace(have.Title) == sec.Title {
				sectionIDs[si] = strings.ToLower(have.ID)
				wantSection[si] = sectionIDs[si]
				sr.SectionID = sectionIDs[si]
				sr.Action = itemPresent
				if sec.Description != "" && sec.Description != have.Description {
					sr.Action = actionUpdate
				}
				break
			}
		}
		result.Sections = append(result.Sections, sr)
	}

	byWork := map[string]api.CollectionItem{}
	for _, it := range items {
		byWork[strings.ToLower(it.EntityID)] = it
	}
	for i := range result.Items {
		r := &result.Items[i]
		if r.Action == itemSkip {
			continue
		}
		have, ok := byWork[r.PublicationID]
		if !ok {
			r.Action = itemAdd
			continue
		}
		r.Action = itemPresent
		if r.note != "" && r.note != have.Note {
			r.Changes = append(r.Changes, "note")
		}
		if r.pin != "" && r.pin != strings.ToLower(have.ExpressionID) {
			r.Changes = append(r.Changes, "edition")
		}
		wantKey := ""
		if r.section >= 0 {
			wantKey = wantSection[r.section]
		}
		if wantKey != strings.ToLower(have.SectionID) {
			r.Changes = append(r.Changes, "section")
		}
		if len(r.Changes) > 0 {
			r.Action = itemUpdate
		}
	}

	final, actual := planOrder(current.Sections, items, result.Items, wantSection)
	result.Reordered = !sameOrder(final, actual)
	result.Unlisted = countUnlisted(items, result.Items)
	if dryRun {
		return result
	}

	if _, err := client.UpdateCollection(ctx, existing.ID, input); err != nil {
		result.setFailure(err)
		return result
	}
	for si, sec := range meta.Sections {
		sr := &result.Sections[si]
		switch sr.Action {
		case actionCreate:
			made, err := client.CreateSection(ctx, existing.ID, api.SectionInput{Title: sec.Title, Description: sec.Description})
			if err != nil {
				result.setFailure(fmt.Errorf("updated, but adding section %q failed: %w", sec.Title, err))
				return result
			}
			sectionIDs[si] = strings.ToLower(made.ID)
			sr.SectionID = sectionIDs[si]
		case actionUpdate:
			if err := client.UpdateSection(ctx, existing.ID, sectionIDs[si], api.SectionInput{Description: sec.Description}); err != nil {
				result.setFailure(fmt.Errorf("updated, but changing section %q failed: %w", sec.Title, err))
				return result
			}
		}
	}
	var adds []api.ItemIn
	for _, r := range result.Items {
		if r.Action == itemAdd {
			adds = append(adds, itemIn(r, sectionIDs))
		}
	}
	if len(adds) > 0 {
		if err := client.AddWorksToCollection(ctx, existing.ID, adds); err != nil {
			result.setFailure(fmt.Errorf("updated, but adding items failed: %w", err))
			return result
		}
	}
	for _, r := range result.Items {
		fields := map[string]any{}
		if slices.Contains(r.Changes, "note") {
			fields["note"] = r.note
		}
		if slices.Contains(r.Changes, "edition") {
			fields["expression_id"] = r.pin
		}
		if len(fields) == 0 {
			continue
		}
		if err := client.UpdateCollectionItem(ctx, existing.ID, r.PublicationID, fields); err != nil {
			result.setFailure(fmt.Errorf("updated, but setting %s failed: %w", r.Path, err))
			return result
		}
	}
	if result.Reordered {
		if err := sendOrder(ctx, client, existing.ID, final, wantSection, sectionIDs, len(current.Sections) == 0 && len(meta.Sections) == 0); err != nil {
			result.setFailure(fmt.Errorf("updated, but reordering items failed: %w", err))
		}
	}
	return result
}

// itemIn is the add body for one listed work.
func itemIn(r itemResult, sectionIDs []string) api.ItemIn {
	in := api.ItemIn{EntityID: r.PublicationID, Note: r.note, ExpressionID: r.pin}
	if r.section >= 0 {
		in.SectionID = sectionIDs[r.section]
	}
	return in
}

// sendOrder puts the planned order on the server. A collection with no
// sections at all takes the flat form; otherwise every section is listed once,
// the sections the manifest created by the id the server gave them.
func sendOrder(ctx context.Context, client *api.Client, id string, final []orderGroup,
	wantSection, sectionIDs []string, flat bool) error {
	if flat {
		var ids []string
		for _, g := range final {
			ids = append(ids, g.ids...)
		}
		return client.ReorderCollection(ctx, id, ids)
	}
	created := map[string]string{}
	for i, key := range wantSection {
		created[key] = sectionIDs[i]
	}
	groups := make([]api.OrderGroup, 0, len(final))
	for _, g := range final {
		group := api.OrderGroup{EntityIDs: append([]string{}, g.ids...)}
		if g.key != "" {
			sid := g.key
			if mapped, ok := created[g.key]; ok {
				sid = mapped
			}
			group.SectionID = &sid
		}
		groups = append(groups, group)
	}
	return client.ReorderCollectionSections(ctx, id, groups)
}

// countUnlisted counts items that no work in the manifest accounts for,
// skipped works included.
func countUnlisted(items []api.CollectionItem, named []itemResult) int {
	listed := make(map[string]bool, len(named))
	for _, l := range named {
		if l.PublicationID != "" {
			listed[l.PublicationID] = true
		}
	}
	n := 0
	for _, item := range items {
		if !listed[strings.ToLower(item.EntityID)] {
			n++
		}
	}
	return n
}

func newCollectionExportCommand(e *env) *cobra.Command {
	var lang, format, output string

	cmd := &cobra.Command{
		Use:   "export <collection>",
		Short: "Write a collection's list as CSL-JSON",
		Long: "Write the collection as a CSL-JSON array, one entry per work in list order, " +
			"each with the owner's `note` and its section as `x-section`. At most 1000 entries. " +
			"Every citation tool reads CSL-JSON; for BibTeX use pandoc:\n\n" +
			"  lt collection export <collection> | pandoc -f csljson -t biblatex\n\n" +
			"<collection> is an id, a /collections/<id> URL or a manifest file.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if format != "csl-json" {
				return fail(ExitBadArguments, "--format %q is not supported; the only format is csl-json "+
					"(convert it with pandoc for BibTeX)", format)
			}
			client, _, err := e.client(cmd.Context(), true, config.ProductLibreTimes)
			if err != nil {
				return err
			}
			id, err := resolveCollection(cmd.Context(), client, args[0])
			if err != nil {
				return err
			}
			data, err := client.ExportCollection(cmd.Context(), id, lang)
			if err != nil {
				return err
			}
			var pretty bytes.Buffer
			if err := json.Indent(&pretty, data, "", "  "); err != nil {
				pretty.Reset()
				pretty.Write(data)
			}
			pretty.WriteByte('\n')
			if output != "" && output != "-" {
				if err := os.WriteFile(output, pretty.Bytes(), 0o644); err != nil {
					return fail(ExitBadArguments, "%s: %v", output, err)
				}
				if !e.quiet {
					fmt.Fprintf(e.err, "wrote %s\n", output)
				}
				return nil
			}
			_, err = e.out.Write(pretty.Bytes())
			return err
		},
	}
	cmd.Flags().StringVar(&lang, "lang", "", "Language the list is read in (the edition shown for a work with no pin).")
	cmd.Flags().StringVar(&format, "format", "csl-json", "Output format: csl-json.")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Write to this file instead of stdout.")
	return cmd
}
