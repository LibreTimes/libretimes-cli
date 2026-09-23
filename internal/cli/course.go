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

// What one lecture of a course resolves to. Additive only, like byline
// credits: `lt course` never removes an item from a collection.
const (
	lectureAdd     = "add"
	lecturePresent = "present"
	lectureSkip    = "skip"
)

// Why a listed lecture was skipped. None of them fails the course: a course is
// published a lecture at a time, so a manifest naming lectures that are not on
// the site yet is the normal state, not an error.
const (
	skipNotPublished = "not_published" // no publication holds this key yet
	skipDeleted      = "deleted"       // a deleted publication holds it
	skipDraft        = "draft"         // a draft holds it; readers could not open it
)

// lectureResult is one lecture's outcome, reported under its course.
type lectureResult struct {
	Path          string `json:"path"`
	ImportKey     string `json:"import_key"`
	Action        string `json:"action"`
	Reason        string `json:"reason,omitempty"`
	PublicationID string `json:"publication_id,omitempty"`
}

// courseResult is one manifest's outcome, and the shape --json emits.
type courseResult struct {
	Path         string          `json:"path"`
	ImportKey    string          `json:"import_key"`
	Action       string          `json:"action"`
	CollectionID string          `json:"collection_id,omitempty"`
	Code         string          `json:"code,omitempty"`
	Message      string          `json:"message,omitempty"`
	Lectures     []lectureResult `json:"lectures,omitempty"`
	// Reordered is true when the collection's order did not match the
	// manifest's, and the run puts it back.
	Reordered bool `json:"reordered,omitempty"`
	// Unlisted counts items in the collection that the manifest does not name.
	// They are left where they are.
	Unlisted int `json:"unlisted,omitempty"`
}

func (r *courseResult) setFailure(err error) {
	r.Action = actionFailed
	r.Code, r.Message = codeAndMessage(err)
}

func (r *courseResult) invalid(format string, args ...any) {
	r.Action = actionFailed
	r.Code = "validation_error"
	r.Message = fmt.Sprintf(format, args...)
}

func newCourseCommand(e *env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "course",
		Short: "Work with courses: the collections that group lecture notes",
	}
	cmd.AddCommand(newCoursePublishCommand(e))
	return cmd
}

func newCoursePublishCommand(e *env) *cobra.Command {
	var (
		dryRun bool
		yes    bool
	)

	cmd := &cobra.Command{
		Use:   "publish <manifest.md>...",
		Short: "Reconcile course manifests as course collections",
		Long: "Reconcile one or more course manifests as LibreTimes course collections.\n\n" +
			"The manifest's frontmatter names the course (`import_key`, `title`, " +
			"`language`) and lists its lecture files in order under `lectures`. Each " +
			"lecture already published with `lt publish` is added to the course, in " +
			"that order; lectures not published yet are skipped and picked up by the " +
			"next run. Nothing is ever removed. Run with --dry-run first.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, err := resolveFiles(args)
			if err != nil {
				return err
			}

			if !dryRun && !yes {
				return fail(ExitNeedsConfirm,
					"course publish would write %d course(s). Re-run with --yes to "+
						"confirm, or --dry-run to see the plan.", len(paths))
			}

			// Required even for --dry-run, as for publish: the plan is read off
			// your own collections and publications.
			client, _, err := e.client(cmd.Context(), true, config.ProductLibreTimes)
			if err != nil {
				return err
			}

			results := make([]courseResult, 0, len(paths))
			failed := false
			for _, path := range paths {
				result := publishCourse(cmd.Context(), client, path, dryRun)
				results = append(results, result)
				if result.Action == actionFailed {
					failed = true
				}
				if !e.jsonOut {
					renderCourse(e, result)
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

func renderCourse(e *env, r courseResult) {
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
	for _, l := range r.Lectures {
		trailing := l.PublicationID
		if l.Action == lectureSkip {
			trailing = l.Reason
		}
		fmt.Fprintf(e.out, "    %-8s %-38s %s\n", l.Action, l.ImportKey, trailing)
	}
	if r.Reordered {
		fmt.Fprintln(e.out, "    reorder  lectures follow the manifest's order")
	}
	if r.Unlisted > 0 {
		fmt.Fprintf(e.out, "    %d item(s) in the collection are not in the manifest; left in place\n", r.Unlisted)
	}
}

// publishCourse resolves and applies one manifest.
//
// Find-then-create, as publish does, never create-then-fallback. Every lecture
// resolves before anything is written, so a manifest naming a missing file
// fails whole rather than creating a course without it.
func publishCourse(ctx context.Context, client *api.Client, path string, dryRun bool) courseResult {
	result := courseResult{Path: path}

	raw, err := os.ReadFile(path)
	if err != nil {
		result.setFailure(err)
		return result
	}
	meta, _, err := frontmatter.Split[frontmatter.CourseMeta](string(raw))
	if err != nil {
		result.setFailure(err)
		return result
	}

	// No default key. A publication's key can fall back to its path, but a
	// manifest is a README in the course's folder, and a key derived from that
	// would name the file rather than the course.
	result.ImportKey = strings.TrimSpace(meta.ImportKey)
	switch {
	case result.ImportKey == "":
		result.invalid("frontmatter has no `import_key`; a course needs one, e.g. <lecturer>/<course>")
		return result
	case strings.TrimSpace(meta.Title) == "":
		result.invalid("frontmatter has no `title`")
		return result
	case strings.TrimSpace(meta.Language) == "":
		result.invalid("frontmatter has no `language`, the language the title and description are written in")
		return result
	case meta.Visibility != "" && !slices.Contains(api.CollectionVisibilityValues, meta.Visibility):
		result.invalid("visibility %q is not one of %s (a course is not a publication: "+
			"there is no private or by_link)", meta.Visibility,
			strings.Join(api.CollectionVisibilityValues, ", "))
		return result
	}

	lectures, err := resolveLectures(ctx, client, path, meta.Lectures)
	if err != nil {
		result.setFailure(err)
		return result
	}
	result.Lectures = lectures

	input := api.CollectionInput{
		Title:           meta.Title,
		Description:     meta.Description,
		PrimaryLanguage: meta.Language,
		Visibility:      meta.Visibility,
		CollectionType:  api.CollectionTypeCourse,
		CategoryIDs:     meta.CategoryIDs,
	}

	existing, err := client.FindCollectionByImportKey(ctx, result.ImportKey)
	if err != nil {
		result.setFailure(err)
		return result
	}

	if existing == nil {
		result.Action = actionCreate
		ids := markAllAdded(result.Lectures)
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
			// a new course on every run, so the one just made is removed.
			result.Code = "unsupported_server"
			result.Action = actionFailed
			result.Message = "this API does not store a collection's import_key yet, so a re-run " +
				"could not find this course again; nothing was kept"
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
				result.setFailure(fmt.Errorf("created, but adding lectures failed: %w", err))
			}
		}
		return result
	}

	result.Action = actionUpdate
	result.CollectionID = existing.ID
	items, err := client.ListCollectionItems(ctx, existing.ID)
	if err != nil {
		result.setFailure(fmt.Errorf("reading the course's items: %w", err))
		return result
	}
	toAdd, desired := diffItems(result.Lectures, items)
	result.Reordered = needsReorder(items, toAdd, desired)
	result.Unlisted = countUnlisted(items, result.Lectures)
	if dryRun {
		return result
	}

	if _, err := client.UpdateCollection(ctx, existing.ID, input); err != nil {
		result.setFailure(err)
		return result
	}
	if len(toAdd) > 0 {
		if err := client.AddPublicationsToCollection(ctx, existing.ID, toAdd); err != nil {
			result.setFailure(fmt.Errorf("updated, but adding lectures failed: %w", err))
			return result
		}
	}
	if result.Reordered {
		if err := client.ReorderCollection(ctx, existing.ID, desired); err != nil {
			result.setFailure(fmt.Errorf("updated, but reordering lectures failed: %w", err))
		}
	}
	return result
}

// resolveLectures reads each listed lecture file for its import key and finds
// the publication holding it. Paths are relative to the manifest.
func resolveLectures(ctx context.Context, client *api.Client, manifest string, listed []string) ([]lectureResult, error) {
	dir := filepath.Dir(manifest)
	results := make([]lectureResult, 0, len(listed))
	seen := map[string]string{}

	for _, entry := range listed {
		lecturePath := filepath.FromSlash(entry)
		if !filepath.IsAbs(lecturePath) {
			lecturePath = filepath.Join(dir, lecturePath)
		}
		raw, err := os.ReadFile(lecturePath)
		if err != nil {
			return nil, fmt.Errorf("lecture %s: %w", entry, err)
		}
		meta, _, err := frontmatter.Split[frontmatter.Meta](string(raw))
		if err != nil {
			return nil, fmt.Errorf("lecture %s: %w", entry, err)
		}
		key := strings.TrimSpace(meta.ImportKey)
		if key == "" {
			key = defaultImportKey(lecturePath)
		}
		if first, dup := seen[key]; dup {
			return nil, fmt.Errorf("lectures %s and %s share the import_key %q; list each lecture once",
				first, entry, key)
		}
		seen[key] = entry
		results = append(results, lectureResult{Path: entry, ImportKey: key})
	}

	for i := range results {
		ref, err := client.FindPublicationByImportKey(ctx, results[i].ImportKey)
		if err != nil {
			return nil, fmt.Errorf("lecture %s: %w", results[i].Path, err)
		}
		if ref == nil {
			results[i].Action, results[i].Reason = lectureSkip, skipNotPublished
			continue
		}
		// Kept on a skipped lecture too, so one that was added and later
		// deleted is recognised in the collection rather than counted as an
		// item the manifest does not name.
		results[i].PublicationID = strings.ToLower(ref.ID)
		switch {
		case ref.Removed:
			results[i].Action, results[i].Reason = lectureSkip, skipDeleted
		case ref.IsDraft:
			results[i].Action, results[i].Reason = lectureSkip, skipDraft
		}
	}
	return results, nil
}

// markAllAdded is the create case: every published lecture is new.
func markAllAdded(lectures []lectureResult) []string {
	var ids []string
	for i := range lectures {
		if lectures[i].Action == lectureSkip {
			continue
		}
		lectures[i].Action = lectureAdd
		ids = append(ids, lectures[i].PublicationID)
	}
	return ids
}

// diffItems marks each published lecture present or add, and returns the ids
// to add and the manifest's order of every published lecture.
//
// A skipped lecture already in the collection -- deleted after it was added --
// is reported as skipped and left where it is. It is not part of the desired
// order, so a reorder leaves its position alone.
func diffItems(lectures []lectureResult, items []api.CollectionItem) (toAdd, desired []string) {
	present := make(map[string]bool, len(items))
	for _, item := range items {
		present[strings.ToLower(item.EntityID)] = true
	}
	for i := range lectures {
		if lectures[i].Action == lectureSkip {
			continue
		}
		id := lectures[i].PublicationID
		desired = append(desired, id)
		if present[id] {
			lectures[i].Action = lecturePresent
		} else {
			lectures[i].Action = lectureAdd
			toAdd = append(toAdd, id)
		}
	}
	return toAdd, desired
}

// needsReorder reports whether, after appending toAdd, the manifest's lectures
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

// countUnlisted counts items that no lecture in the manifest accounts for,
// skipped lectures included.
func countUnlisted(items []api.CollectionItem, lectures []lectureResult) int {
	listed := make(map[string]bool, len(lectures))
	for _, l := range lectures {
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
