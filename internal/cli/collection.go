package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
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
	itemSkip    = "skip"
)

// Why a listed work was skipped. None of them fails the collection: works are
// published one at a time, so a manifest naming works that are not on the site
// yet is the normal state, not an error.
const (
	skipNotPublished = "not_published" // no publication holds this key yet
	skipDeleted      = "deleted"       // a deleted publication holds it
	skipDraft        = "draft"         // a draft holds it; readers could not open it
)

// itemResult is one listed work's outcome, reported under its collection.
type itemResult struct {
	Path          string `json:"path"`
	ImportKey     string `json:"import_key"`
	Action        string `json:"action"`
	Reason        string `json:"reason,omitempty"`
	PublicationID string `json:"publication_id,omitempty"`
}

// collectionResult is one manifest's outcome, and the shape --json emits.
type collectionResult struct {
	Path           string       `json:"path"`
	ImportKey      string       `json:"import_key"`
	CollectionType string       `json:"collection_type,omitempty"`
	Action         string       `json:"action"`
	CollectionID   string       `json:"collection_id,omitempty"`
	Code           string       `json:"code,omitempty"`
	Message        string       `json:"message,omitempty"`
	Items          []itemResult `json:"items,omitempty"`
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
			"order under `items`. Each work already published with `lt work publish` is " +
			"added, in that order; works not published yet, or still drafts, are skipped " +
			"and picked up by the next run. Nothing is ever removed. Run with --dry-run first.",
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
	for _, l := range r.Items {
		trailing := l.PublicationID
		if l.Action == itemSkip {
			trailing = l.Reason
		}
		fmt.Fprintf(e.out, "    %-8s %-38s %s\n", l.Action, l.ImportKey, trailing)
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
				for i, item := range items {
					fmt.Fprintf(w, "  %3d. %s %s\n", i+1, item.EntityType, item.EntityID)
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
	switch {
	case result.ImportKey == "":
		result.invalid("frontmatter has no `import_key`; a collection needs one, e.g. <author>/<course>")
		return result
	case result.CollectionType == "":
		result.invalid("frontmatter has no `collection_type`, which is required and has no default (one of %s)",
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

	listed, err := resolveItems(ctx, client, path, meta.Items)
	if err != nil {
		result.setFailure(err)
		return result
	}
	result.Items = listed

	input := api.CollectionInput{
		Title:           meta.Title,
		Description:     meta.Description,
		PrimaryLanguage: meta.Language,
		Visibility:      meta.Visibility,
		CollectionType:  result.CollectionType,
		CategoryIDs:     meta.CategoryIDs,
	}

	existing, err := client.FindCollectionByImportKey(ctx, result.ImportKey)
	if err != nil {
		result.setFailure(err)
		return result
	}

	if existing == nil {
		result.Action = actionCreate
		ids := markAllAdded(result.Items)
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
		// Appended in manifest order, so positions already follow it.
		if len(ids) > 0 {
			if err := client.AddPublicationsToCollection(ctx, created.ID, ids); err != nil {
				result.setFailure(fmt.Errorf("created, but adding items failed: %w", err))
			}
		}
		return result
	}

	result.Action = actionUpdate
	result.CollectionID = existing.ID
	items, err := client.ListCollectionItems(ctx, existing.ID)
	if err != nil {
		result.setFailure(fmt.Errorf("reading the collection's items: %w", err))
		return result
	}
	toAdd, desired := diffItems(result.Items, items)
	result.Reordered = needsReorder(items, toAdd, desired)
	result.Unlisted = countUnlisted(items, result.Items)
	if dryRun {
		return result
	}

	if _, err := client.UpdateCollection(ctx, existing.ID, input); err != nil {
		result.setFailure(err)
		return result
	}
	if len(toAdd) > 0 {
		if err := client.AddPublicationsToCollection(ctx, existing.ID, toAdd); err != nil {
			result.setFailure(fmt.Errorf("updated, but adding items failed: %w", err))
			return result
		}
	}
	if result.Reordered {
		if err := client.ReorderCollection(ctx, existing.ID, desired); err != nil {
			result.setFailure(fmt.Errorf("updated, but reordering items failed: %w", err))
		}
	}
	return result
}

// resolveItems reads each listed work file for its import key and finds the
// publication holding it. Paths are relative to the manifest.
func resolveItems(ctx context.Context, client *api.Client, manifest string, listed []string) ([]itemResult, error) {
	dir := filepath.Dir(manifest)
	results := make([]itemResult, 0, len(listed))
	seen := map[string]string{}

	for _, entry := range listed {
		itemPath := filepath.FromSlash(entry)
		if !filepath.IsAbs(itemPath) {
			itemPath = filepath.Join(dir, itemPath)
		}
		raw, err := os.ReadFile(itemPath)
		if err != nil {
			return nil, fmt.Errorf("item %s: %w", entry, err)
		}
		meta, _, err := frontmatter.Split[frontmatter.Meta](string(raw))
		if err != nil {
			return nil, fmt.Errorf("item %s: %w", entry, err)
		}
		key := strings.TrimSpace(meta.ImportKey)
		if key == "" {
			key = defaultImportKey(itemPath)
		}
		if first, dup := seen[key]; dup {
			return nil, fmt.Errorf("items %s and %s share the import_key %q; list each work once",
				first, entry, key)
		}
		seen[key] = entry
		results = append(results, itemResult{Path: entry, ImportKey: key})
	}

	for i := range results {
		ref, err := client.FindPublicationByImportKey(ctx, results[i].ImportKey)
		if err != nil {
			return nil, fmt.Errorf("item %s: %w", results[i].Path, err)
		}
		if ref == nil {
			results[i].Action, results[i].Reason = itemSkip, skipNotPublished
			continue
		}
		// Kept on a skipped item too, so one that was added and later
		// deleted is recognised in the collection rather than counted as an
		// item the manifest does not name.
		results[i].PublicationID = strings.ToLower(ref.ID)
		switch {
		case ref.Removed:
			results[i].Action, results[i].Reason = itemSkip, skipDeleted
		case ref.IsDraft:
			results[i].Action, results[i].Reason = itemSkip, skipDraft
		}
	}
	return results, nil
}

// markAllAdded is the create case: every published work is new.
func markAllAdded(listed []itemResult) []string {
	var ids []string
	for i := range listed {
		if listed[i].Action == itemSkip {
			continue
		}
		listed[i].Action = itemAdd
		ids = append(ids, listed[i].PublicationID)
	}
	return ids
}

// diffItems marks each published work present or add, and returns the ids to
// add and the manifest's order of every published work.
//
// A skipped work already in the collection -- deleted after it was added -- is
// reported as skipped and left where it is. It is not part of the desired
// order, so a reorder leaves its position alone.
func diffItems(listed []itemResult, items []api.CollectionItem) (toAdd, desired []string) {
	present := make(map[string]bool, len(items))
	for _, item := range items {
		present[strings.ToLower(item.EntityID)] = true
	}
	for i := range listed {
		if listed[i].Action == itemSkip {
			continue
		}
		id := listed[i].PublicationID
		desired = append(desired, id)
		if present[id] {
			listed[i].Action = itemPresent
		} else {
			listed[i].Action = itemAdd
			toAdd = append(toAdd, id)
		}
	}
	return toAdd, desired
}

// needsReorder reports whether, after appending toAdd, the manifest's works
// would sit in the collection in an order other than the manifest's. Items the
// manifest does not name are ignored: they keep their positions either way.
func needsReorder(items []api.CollectionItem, toAdd, desired []string) bool {
	wanted := make(map[string]bool, len(desired))
	for _, id := range desired {
		wanted[id] = true
	}
	var after []string
	for _, item := range items {
		if id := strings.ToLower(item.EntityID); wanted[id] {
			after = append(after, id)
		}
	}
	after = append(after, toAdd...)
	return !slices.Equal(after, desired)
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
