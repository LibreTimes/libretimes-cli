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

// What one node of a book's contents resolves to.
const (
	chapterCreate    = "create"
	chapterUpdate    = "update"
	chapterUnchanged = "unchanged"
	chapterDelete    = "delete"   // unlisted, and --prune was given
	chapterUnlisted  = "unlisted" // unlisted, and the run refuses because of it
)

// The per-division limits the API enforces, checked here so the failure names
// the file rather than relaying a 422 about "a chapter".
const (
	maxChapterBytes = 100_000
	maxRefLength    = 64
)

// chapterResult is one node's outcome, in reading order, with its depth in the
// tree so a flat list still reads as a contents page.
type chapterResult struct {
	File          string   `json:"file,omitempty"`
	Depth         int      `json:"depth"`
	Kind          string   `json:"kind"`
	Label         string   `json:"label,omitempty"`
	Title         string   `json:"title,omitempty"`
	Ref           string   `json:"ref,omitempty"`
	Action        string   `json:"action"`
	Changes       []string `json:"changes,omitempty"`
	DivisionID    string   `json:"division_id,omitempty"`
	ReadingNumber *int     `json:"reading_number,omitempty"`
}

// bookResult is one manifest's outcome, and the shape --json emits. Shaped like
// courseResult, so a script that reads one reads the other.
type bookResult struct {
	Path          string              `json:"path"`
	ImportKey     string              `json:"import_key"`
	Action        string              `json:"action"`
	PublicationID string              `json:"publication_id,omitempty"`
	ExpressionID  string              `json:"expression_id,omitempty"`
	Code          string              `json:"code,omitempty"`
	Message       string              `json:"message,omitempty"`
	Contributors  []contributorResult `json:"contributors,omitempty"`
	Chapters      []chapterResult     `json:"chapters,omitempty"`
	// Unlisted are the divisions on the site that the manifest does not name.
	// Deleted with --prune; otherwise the run refuses and writes nothing.
	Unlisted []chapterResult `json:"unlisted,omitempty"`
	// Published is true when this run took the book out of draft.
	Published bool     `json:"published,omitempty"`
	Warnings  []string `json:"warnings,omitempty"`
}

func (r *bookResult) setFailure(err error) {
	r.Action = actionFailed
	r.Code, r.Message = codeAndMessage(err)
}

func (r *bookResult) invalid(format string, args ...any) {
	r.Action = actionFailed
	r.Code = "validation_error"
	r.Message = fmt.Sprintf(format, args...)
}

func newBookCommand(e *env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "book",
		Short: "Work with books: a text read one chapter per page",
	}
	cmd.AddCommand(newBookPublishCommand(e))
	return cmd
}

func newBookPublishCommand(e *env) *cobra.Command {
	var (
		dryRun bool
		yes    bool
		prune  bool
	)

	cmd := &cobra.Command{
		Use:   "publish <book.md>...",
		Short: "Reconcile book manifests as books",
		Long: "Reconcile one or more book manifests as LibreTimes books.\n\n" +
			"The manifest's frontmatter names the book (`import_key`, `title`, " +
			"`language`) and lists its contents as a tree under `contents`, each " +
			"chapter's text in a file of its own. The manifest is authoritative for " +
			"the book's order: chapters are created, updated and moved to match it. " +
			"A chapter on the site that the manifest no longer names stops the run " +
			"unless --prune is given, which deletes it. Run with --dry-run first.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, err := resolveFiles(args)
			if err != nil {
				return err
			}

			if !dryRun && !yes {
				return fail(ExitNeedsConfirm,
					"book publish would write %d book(s). Re-run with --yes to "+
						"confirm, or --dry-run to see the plan.", len(paths))
			}

			// Required even for --dry-run, as for publish: the plan is read off
			// your own publications.
			client, _, err := e.client(cmd.Context(), true, config.ProductLibreTimes)
			if err != nil {
				return err
			}

			results := make([]bookResult, 0, len(paths))
			failed := false
			profiles := newProfileResolver(client)
			for _, path := range paths {
				result := publishBook(cmd.Context(), client, path, bookOptions{
					dryRun: dryRun, prune: prune, profiles: profiles,
				})
				results = append(results, result)
				if result.Action == actionFailed || result.Action == actionBlocked {
					failed = true
				}
				if !e.jsonOut {
					renderBook(e, result)
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
	cmd.Flags().BoolVar(&prune, "prune", false,
		"Delete chapters on the site that the manifest no longer lists. On a published "+
			"book their reading numbers are retired, never reissued.")
	return cmd
}

func renderBook(e *env, r bookResult) {
	if r.Action == actionFailed || r.Action == actionBlocked {
		fmt.Fprintf(e.err, "  %-9s %-40s %s: %s\n", r.Action, r.ImportKey, r.Code, r.Message)
	} else {
		if e.quiet {
			return
		}
		fmt.Fprintf(e.out, "  %-9s %-40s %s\n", r.Action, r.ImportKey, r.PublicationID)
	}
	for _, c := range r.Contributors {
		fmt.Fprintf(e.out, "    %-9s %-10s %s\n", c.Action, c.Role, c.Profile)
	}
	for _, c := range append(slices.Clone(r.Chapters), r.Unlisted...) {
		name := strings.TrimSpace(c.Label + " " + c.Title)
		if name == "" {
			name = "(" + c.Kind + ")"
		}
		trailing := c.File
		if len(c.Changes) > 0 {
			trailing = strings.TrimSpace(trailing + " [" + strings.Join(c.Changes, ", ") + "]")
		}
		if c.ReadingNumber != nil {
			trailing = strings.TrimSpace(fmt.Sprintf("%s /read/%d", trailing, *c.ReadingNumber))
		}
		fmt.Fprintf(e.out, "    %-9s %s%-*s %s\n", c.Action, strings.Repeat("  ", c.Depth),
			max(1, 36-2*c.Depth), name, trailing)
	}
	if r.Published {
		fmt.Fprintln(e.out, "    publish   the book is out of draft")
	}
	for _, w := range r.Warnings {
		fmt.Fprintf(e.err, "    warning: %s\n", w)
	}
}

type bookOptions struct {
	dryRun   bool
	prune    bool
	profiles *profileResolver
}

// planNode is one node of the manifest's contents, with its text read and its
// fields settled between the manifest and the chapter file.
type planNode struct {
	file      string
	kind      string
	label     string
	title     string
	ref       string
	firstLine string
	body      *string // nil: navigation, no page of its own
	children  []*planNode

	match  *siteNode
	result int // index into bookResult.Chapters
}

// siteNode is one division already on the site.
type siteNode struct {
	node     api.DivisionNode
	parentID string // "" at the root level
	claimed  bool
}

// publishBook resolves and applies one manifest.
//
// Everything is read and matched before anything is written, as for a course:
// a manifest naming a missing file, a bad kind or a duplicate ref fails whole,
// and a dry run reports exactly the plan the real run carries out.
func publishBook(ctx context.Context, client *api.Client, path string, opts bookOptions) bookResult {
	result := bookResult{Path: path}
	if opts.profiles == nil {
		opts.profiles = newProfileResolver(client)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		result.setFailure(err)
		return result
	}
	meta, _, err := frontmatter.Split[frontmatter.BookMeta](string(raw))
	if err != nil {
		result.setFailure(err)
		return result
	}

	// No default key, for the course's reason: a key derived from `book.md`
	// would name the file rather than the book.
	result.ImportKey = strings.TrimSpace(meta.ImportKey)
	switch {
	case result.ImportKey == "":
		result.invalid("frontmatter has no `import_key`; a book needs one, e.g. <author>/<book>")
		return result
	case meta.Type != "" && meta.Type != api.TypeBook:
		result.invalid("`type` is %q; a book manifest is always a book (leave `type` out), "+
			"and a single-document text is published with `lt publish`", meta.Type)
		return result
	case strings.TrimSpace(meta.Title) == "":
		result.invalid("frontmatter has no `title`")
		return result
	case strings.TrimSpace(meta.Language) == "":
		result.invalid("frontmatter has no `language`, the language the book is written in")
		return result
	case meta.Visibility != "" && !slices.Contains(api.VisibilityValues, meta.Visibility):
		result.invalid("visibility %q is not one of %s", meta.Visibility,
			strings.Join(api.VisibilityValues, ", "))
		return result
	case len(meta.Contents) == 0:
		result.invalid("`contents` is empty; list the book's chapters, each with its `file`")
		return result
	}

	plan, warnings, err := readContents(filepath.Dir(path), meta.Contents)
	if err != nil {
		result.invalid("%v", err)
		return result
	}
	result.Warnings = warnings

	credits, err := resolveContributors(ctx, opts.profiles, meta.Contributors)
	if err != nil {
		result.setFailure(err)
		return result
	}

	existing, err := client.FindPublicationByImportKey(ctx, result.ImportKey)
	if err != nil {
		result.setFailure(err)
		return result
	}

	var (
		info *api.BookInfo
		site []*siteNode
	)
	switch {
	case existing != nil && existing.Removed:
		result.Action = actionBlocked
		result.Code = "blocked"
		result.PublicationID = existing.ID
		result.Message = fmt.Sprintf(
			"a deleted publication (%s) holds this key; restore it or use another key", existing.ID)
		return result

	case existing != nil:
		result.Action = actionUpdate
		result.PublicationID = existing.ID
		info, err = client.GetPublication(ctx, existing.ID)
		if err != nil {
			result.setFailure(err)
			return result
		}
		if info.Type != api.TypeBook {
			result.Action = actionFailed
			result.Code = "not_a_book"
			result.Message = fmt.Sprintf("this key belongs to a %s, not a book; a publication "+
				"cannot change into a book, so use another key", info.Type)
			return result
		}
		result.ExpressionID = bookExpression(info, meta.Language)
		if result.ExpressionID == "" {
			result.Action = actionFailed
			result.Code = "no_expression"
			result.Message = fmt.Sprintf("the book is written in %s and has no %s text to update; "+
				"add the %s translation on the site first, then re-run (lt does not create translations)",
				info.Language, meta.Language, meta.Language)
			return result
		}
		if len(credits) > 0 {
			byline, err := client.ListAuthors(ctx, existing.ID)
			if err != nil {
				result.setFailure(fmt.Errorf("reading the byline: %w", err))
				return result
			}
			if err := diffByline(credits, byline); err != nil {
				result.setFailure(err)
				return result
			}
			result.Contributors = credits
		}
		contents, err := client.GetContents(ctx, existing.ID, result.ExpressionID)
		if err != nil {
			result.setFailure(fmt.Errorf("reading the book's contents: %w", err))
			return result
		}
		site = flattenSite(contents.Tree, "")

	default:
		result.Action = actionCreate
		result.Contributors = credits
	}

	matchContents(plan, site)
	result.Chapters = planResults(plan, 0, nil)
	for _, s := range site {
		if !s.claimed {
			result.Unlisted = append(result.Unlisted, siteResult(s, site, opts.prune))
		}
	}

	if err := diffChapters(ctx, client, &result, plan); err != nil {
		result.setFailure(fmt.Errorf("reading a chapter: %w", err))
		return result
	}

	if len(result.Unlisted) > 0 && !opts.prune {
		names := make([]string, 0, len(result.Unlisted))
		for _, u := range result.Unlisted {
			names = append(names, strings.TrimSpace(u.Label+" "+u.Title+" ("+u.Kind+")"))
		}
		result.Action = actionFailed
		result.Code = "unlisted_chapters"
		result.Message = fmt.Sprintf("%d division(s) on the site are not in the manifest: %s. "+
			"Add them to `contents`, or re-run with --prune to delete them",
			len(result.Unlisted), strings.Join(names, "; "))
		return result
	}

	if opts.dryRun {
		if err := newTreeWriter(ctx, client, &result, site, true).write(plan, ""); err != nil {
			result.setFailure(err)
		}
		return result
	}

	input := api.PublicationInput{
		Title:       meta.Title,
		Description: meta.Description,
		Type:        api.TypeBook,
		License:     meta.License,
		Language:    meta.Language,
		Visibility:  meta.Visibility,
		AIUsage:     meta.AIUsage,
		Tags:        meta.Tags,
		CategoryIDs: meta.CategoryIDs,
	}

	if result.Action == actionCreate {
		// A book is created as a draft whatever the manifest says: it has no
		// chapters yet, and the API refuses to publish an empty one. It is
		// published below, once the tree is written.
		draft := true
		input.IsDraft = &draft
		input.ImportKey = result.ImportKey
		applyToCreate(&input, credits)
		created, err := client.CreatePublication(ctx, input)
		if err != nil {
			result.setFailure(err)
			return result
		}
		result.PublicationID = created.ID
		info, err = client.GetPublication(ctx, created.ID)
		if err != nil {
			result.setFailure(fmt.Errorf("created %s, but reading it back failed: %w", created.ID, err))
			return result
		}
		result.ExpressionID = bookExpression(info, meta.Language)
		if result.ExpressionID == "" {
			result.Action = actionFailed
			result.Code = "no_expression"
			result.Message = fmt.Sprintf("created %s, but the API returned no expression to write "+
				"its chapters to; this API may predate authored books", created.ID)
			return result
		}
	} else {
		if _, err := client.UpdatePublication(ctx, result.PublicationID, input); err != nil {
			result.setFailure(err)
			return result
		}
		if err := inviteMissing(ctx, client, result.PublicationID, credits); err != nil {
			result.setFailure(err)
			return result
		}
	}

	w := newTreeWriter(ctx, client, &result, site, false)
	if err := w.write(plan, ""); err != nil {
		result.setFailure(fmt.Errorf("writing the contents: %w", err))
		return result
	}
	if err := w.prune(site); err != nil {
		result.setFailure(fmt.Errorf("deleting unlisted chapters: %w", err))
		return result
	}

	// Out of draft last, once there is text to read. An update leaves the
	// draft state alone unless the manifest says what it should be.
	wantDraft := meta.IsDraft != nil && *meta.IsDraft
	if (result.Action == actionCreate && !wantDraft) ||
		(result.Action == actionUpdate && meta.IsDraft != nil && *meta.IsDraft != info.IsDraft) {
		if _, err := client.UpdatePublication(ctx, result.PublicationID,
			api.PublicationInput{IsDraft: &wantDraft}); err != nil {
			result.setFailure(fmt.Errorf("the chapters are written, but changing the draft state failed: %w", err))
			return result
		}
		result.Published = !wantDraft
	}

	// Reading numbers are the server's to assign, and they settle in reading
	// order only once the whole tree is written, so they are read back last.
	if contents, err := client.GetContents(ctx, result.PublicationID, result.ExpressionID); err == nil {
		numbers := map[string]*int{}
		for _, s := range flattenSite(contents.Tree, "") {
			numbers[s.node.ID] = s.node.ReadingNumber
		}
		for i := range result.Chapters {
			result.Chapters[i].ReadingNumber = numbers[result.Chapters[i].DivisionID]
		}
	}
	return result
}

// bookExpression picks the expression whose tree the manifest describes: the
// primary for the manifest's language, else for the book's own language, else
// the only one there is.
func bookExpression(info *api.BookInfo, language string) string {
	// The manifest's language, exactly. Falling back to the book's own
	// language, or to its only text, was right while a book had one text; since
	// a book can carry translations it would write an English manifest over the
	// Russian original, so a missing language is a refusal, never a guess.
	return info.PrimaryExpressionByLanguage[language]
}

// readContents turns the manifest's entries into a plan, reading each file and
// settling each field between the manifest and the file's own frontmatter.
func readContents(dir string, entries []frontmatter.ContentEntry) ([]*planNode, []string, error) {
	refs := map[string]string{}
	files := map[string]bool{}
	var withRef, total int

	var walk func(entries []frontmatter.ContentEntry, where string) ([]*planNode, error)
	walk = func(entries []frontmatter.ContentEntry, where string) ([]*planNode, error) {
		nodes := make([]*planNode, 0, len(entries))
		for i, entry := range entries {
			at := fmt.Sprintf("%s[%d]", where, i)
			node := &planNode{
				file: entry.File, kind: entry.Kind, label: entry.Label,
				title: entry.Title, ref: entry.Ref, firstLine: entry.FirstLine,
			}
			if entry.File != "" {
				at = entry.File
				if files[entry.File] {
					return nil, fmt.Errorf("%s is listed twice in `contents`", entry.File)
				}
				files[entry.File] = true
				chapterPath := filepath.FromSlash(entry.File)
				if !filepath.IsAbs(chapterPath) {
					chapterPath = filepath.Join(dir, chapterPath)
				}
				raw, err := os.ReadFile(chapterPath)
				if err != nil {
					return nil, fmt.Errorf("chapter %s: %w", entry.File, err)
				}
				chapter, body, err := frontmatter.Split[frontmatter.ChapterMeta](string(raw))
				if err != nil {
					return nil, fmt.Errorf("chapter %s: %w", entry.File, err)
				}
				for _, f := range []struct {
					name     string
					dst      *string
					fromFile string
				}{
					{"kind", &node.kind, chapter.Kind},
					{"label", &node.label, chapter.Label},
					{"title", &node.title, chapter.Title},
					{"ref", &node.ref, chapter.Ref},
					{"first_line", &node.firstLine, chapter.FirstLine},
				} {
					switch {
					case f.fromFile == "":
					case *f.dst == "":
						*f.dst = f.fromFile
					case *f.dst != f.fromFile:
						return nil, fmt.Errorf("%s: `%s` is %q in the manifest and %q in the file; "+
							"write it in one place", entry.File, f.name, *f.dst, f.fromFile)
					}
				}
				if len(body) > maxChapterBytes {
					return nil, fmt.Errorf("%s is %d KB; a chapter is one page, at most %d KB",
						entry.File, len(body)/1000, maxChapterBytes/1000)
				}
				if strings.TrimSpace(body) != "" {
					node.body = &body
				}
			}
			if node.kind == "" {
				node.kind = "chapter"
				if entry.File == "" && len(entry.Children) > 0 {
					node.kind = "part"
				}
			}
			if !slices.Contains(api.DivisionKinds, node.kind) {
				return nil, fmt.Errorf("%s: kind %q is not one of %s", at, node.kind,
					strings.Join(api.DivisionKinds, ", "))
			}
			if node.ref != "" {
				if len(node.ref) > maxRefLength {
					return nil, fmt.Errorf("%s: ref %q is longer than %d characters", at, node.ref, maxRefLength)
				}
				if first, dup := refs[node.ref]; dup {
					return nil, fmt.Errorf("%s and %s share the ref %q; a ref names one place in the book",
						first, at, node.ref)
				}
				refs[node.ref] = at
				if entry.File != "" {
					withRef++
				}
			}
			if entry.File == "" && len(entry.Children) == 0 && node.label == "" && node.title == "" {
				return nil, fmt.Errorf("%s has no file, no children and no label; it would be an "+
					"empty line in the contents", at)
			}
			// Only chapters count toward the warning: a heading with no text of
			// its own has nothing to lose when it is matched by position.
			if entry.File != "" {
				total++
			}
			children, err := walk(entry.Children, at+".children")
			if err != nil {
				return nil, err
			}
			node.children = children
			nodes = append(nodes, node)
		}
		return nodes, nil
	}

	plan, err := walk(entries, "contents")
	if err != nil {
		return nil, nil, err
	}
	var warnings []string
	if withRef > 0 && withRef < total {
		warnings = append(warnings, fmt.Sprintf("%d of %d chapters carry a `ref`; the rest are matched "+
			"to the site by position, so reordering them moves text between chapters rather than "+
			"moving chapters", withRef, total))
	}
	return plan, warnings, nil
}

// flattenSite lists the site's divisions depth-first, each knowing its parent.
func flattenSite(tree []api.DivisionNode, parentID string) []*siteNode {
	var out []*siteNode
	for _, node := range tree {
		out = append(out, &siteNode{node: node, parentID: parentID})
		out = append(out, flattenSite(node.Children, node.ID)...)
	}
	return out
}

// matchContents pairs each manifest entry with a division on the site: by ref
// wherever a ref is written, else by position -- the entry at index i under a
// matched parent takes the site's i-th child there, if that child has no ref
// of its own and nothing else claimed it. An unmatched entry is created.
func matchContents(plan []*planNode, site []*siteNode) {
	byRef := map[string]*siteNode{}
	children := map[string][]*siteNode{}
	for _, s := range site {
		if s.node.Ref != nil && *s.node.Ref != "" {
			byRef[*s.node.Ref] = s
		}
		children[s.parentID] = append(children[s.parentID], s)
	}

	var byRefPass func(nodes []*planNode)
	byRefPass = func(nodes []*planNode) {
		for _, n := range nodes {
			if s := byRef[n.ref]; n.ref != "" && s != nil {
				n.match, s.claimed = s, true
			}
			byRefPass(n.children)
		}
	}
	byRefPass(plan)

	var byPosition func(nodes []*planNode, parentID string, parentOnSite bool)
	byPosition = func(nodes []*planNode, parentID string, parentOnSite bool) {
		for i, n := range nodes {
			if n.match == nil && n.ref == "" && parentOnSite && i < len(children[parentID]) {
				s := children[parentID][i]
				if !s.claimed && (s.node.Ref == nil || *s.node.Ref == "") {
					n.match, s.claimed = s, true
				}
			}
			if n.match != nil {
				byPosition(n.children, n.match.node.ID, true)
			} else {
				byPosition(n.children, "", false)
			}
		}
	}
	byPosition(plan, "", true)
}

// planResults flattens the plan into results, depth-first, recording on each
// node where its result sits.
func planResults(nodes []*planNode, depth int, out []chapterResult) []chapterResult {
	for _, n := range nodes {
		n.result = len(out)
		r := chapterResult{
			File: n.file, Depth: depth, Kind: n.kind, Label: n.label, Title: n.title, Ref: n.ref,
			Action: chapterCreate,
		}
		if n.match != nil {
			r.DivisionID = n.match.node.ID
			r.ReadingNumber = n.match.node.ReadingNumber
		}
		out = append(out, r)
		out = planResults(n.children, depth+1, out)
	}
	return out
}

func siteResult(s *siteNode, site []*siteNode, prune bool) chapterResult {
	depth := 0
	parents := map[string]string{}
	for _, other := range site {
		parents[other.node.ID] = other.parentID
	}
	for p := s.parentID; p != ""; p = parents[p] {
		depth++
	}
	action := chapterUnlisted
	if prune {
		action = chapterDelete
	}
	return chapterResult{
		Depth: depth, Kind: s.node.Kind, Label: s.node.Label, Title: deref(s.node.Title),
		Ref: deref(s.node.Ref), Action: action, DivisionID: s.node.ID,
		ReadingNumber: s.node.ReadingNumber,
	}
}

// diffChapters settles which of each matched node's fields differ. A body is
// read only where one could differ -- a division with no reading number has
// no body. Whether a node moves is the writer's to say (treeWriter.write).
func diffChapters(ctx context.Context, client *api.Client, result *bookResult, plan []*planNode) error {
	var walk func(nodes []*planNode) error
	walk = func(nodes []*planNode) error {
		for _, n := range nodes {
			if n.match == nil {
				if err := walk(n.children); err != nil {
					return err
				}
				continue
			}
			r := &result.Chapters[n.result]
			s := n.match.node
			var changes []string
			if s.Kind != n.kind {
				changes = append(changes, "kind")
			}
			if s.Label != n.label {
				changes = append(changes, "label")
			}
			if deref(s.Title) != n.title {
				changes = append(changes, "title")
			}
			if deref(s.FirstLine) != n.firstLine {
				changes = append(changes, "first_line")
			}
			if deref(s.Ref) != n.ref {
				changes = append(changes, "ref")
			}
			if n.body != nil || s.ReadingNumber != nil {
				division, err := client.GetDivision(ctx, result.PublicationID, result.ExpressionID, s.ID)
				if err != nil {
					return err
				}
				if strings.TrimSpace(deref(division.BodyMD)) != strings.TrimSpace(deref(n.body)) {
					changes = append(changes, "body")
				}
			}
			r.Changes = changes
			r.Action = chapterUnchanged
			if len(changes) > 0 {
				r.Action = chapterUpdate
			}
			if err := walk(n.children); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(plan)
}

// treeWriter applies the plan, keeping a local copy of each parent's child
// order so it moves a node only when the server's order differs.
type treeWriter struct {
	// dry walks the same placements without calling the API, which is how a
	// dry run reports exactly the moves the real run makes.
	dry        bool
	ctx        context.Context
	client     *api.Client
	result     *bookResult
	work       string
	expression string
	children   map[string][]string // parent id ("" = root) -> child ids, in order
	parentOf   map[string]string
	created    int
}

func newTreeWriter(ctx context.Context, client *api.Client, result *bookResult, site []*siteNode, dry bool) *treeWriter {
	w := &treeWriter{
		dry: dry, ctx: ctx, client: client, result: result,
		work: result.PublicationID, expression: result.ExpressionID,
		children: map[string][]string{},
		parentOf: map[string]string{},
	}
	for _, s := range site {
		w.children[s.parentID] = append(w.children[s.parentID], s.node.ID)
		w.parentOf[s.node.ID] = s.parentID
	}
	return w
}

// write places nodes under parentID in manifest order, top-down. Placing a
// parent before its children means every node's final ancestors are already
// where they belong when it moves, so a move can never form a cycle.
func (w *treeWriter) write(nodes []*planNode, parentID string) error {
	var parent *string
	if parentID != "" {
		parent = &parentID
	}
	for i, n := range nodes {
		r := &w.result.Chapters[n.result]
		var id string
		if n.match == nil {
			if w.dry {
				w.created++
				id = fmt.Sprintf("planned-%d", w.created)
			} else {
				created, err := w.client.CreateDivision(w.ctx, w.work, w.expression, api.DivisionCreate{
					Kind: n.kind, Label: n.label, Title: optional(n.title),
					FirstLine: optional(n.firstLine), Ref: optional(n.ref),
					ParentID: parent, Index: i,
				})
				if err != nil {
					return fmt.Errorf("%s: %w", describeNode(n), err)
				}
				id = created.ID
				r.DivisionID = id
				if n.body != nil {
					if err := w.writeBody(n, id, map[string]any{"body_md": *n.body}); err != nil {
						return err
					}
				}
			}
			w.insert(id, parentID, i)
		} else {
			id = n.match.node.ID
			fields := map[string]any{}
			for _, change := range r.Changes {
				switch change {
				case "kind":
					fields["kind"] = n.kind
				case "label":
					fields["label"] = n.label
				case "title":
					fields["title"] = optional(n.title)
				case "first_line":
					fields["first_line"] = optional(n.firstLine)
				case "ref":
					fields["ref"] = optional(n.ref)
				case "body":
					fields["body_md"] = n.body
				}
			}
			if len(fields) > 0 && !w.dry {
				if err := w.writeBody(n, id, fields); err != nil {
					return err
				}
			}
			if w.parentOf[id] != parentID || w.indexOf(id) != i {
				r.Changes = append(r.Changes, "position")
				r.Action = chapterUpdate
				if !w.dry {
					if err := w.client.MoveDivision(w.ctx, w.work, w.expression, id,
						api.DivisionMove{ParentID: parent, Index: i}); err != nil {
						return fmt.Errorf("%s: moving it: %w", describeNode(n), err)
					}
				}
				w.remove(id)
				w.insert(id, parentID, i)
			}
		}
		if err := w.write(n.children, id); err != nil {
			return err
		}
	}
	return nil
}

// writeBody patches a node and notes when the platform stored a different
// body from the one sent: it sanitises markup and shifts headings so none sits
// above H2, and a file it rewrote will report `update` on every run until the
// file matches what is stored.
func (w *treeWriter) writeBody(n *planNode, id string, fields map[string]any) error {
	updated, err := w.client.UpdateDivision(w.ctx, w.work, w.expression, id, fields)
	if err != nil {
		return fmt.Errorf("%s: %w", describeNode(n), err)
	}
	if _, sent := fields["body_md"]; sent && n.body != nil &&
		strings.TrimSpace(deref(updated.BodyMD)) != strings.TrimSpace(*n.body) {
		w.result.Warnings = append(w.result.Warnings, fmt.Sprintf("%s: the platform stored this "+
			"chapter's markdown with changes (headings start at ##, and unsafe markup is removed); "+
			"until the file matches, every run reports it as an update", describeNode(n)))
	}
	return nil
}

// prune deletes the unlisted divisions whose parent is kept. Everything the
// manifest names has already moved out from under them, so each deletion
// takes only unlisted nodes with it, and an unlisted node inside a deleted
// subtree is not deleted twice.
func (w *treeWriter) prune(site []*siteNode) error {
	claimed := map[string]bool{"": true}
	for _, s := range site {
		if s.claimed {
			claimed[s.node.ID] = true
		}
	}
	for _, s := range site {
		if s.claimed || !claimed[w.parentOf[s.node.ID]] {
			continue
		}
		if w.dry {
			continue
		}
		if err := w.client.DeleteDivision(w.ctx, w.work, w.expression, s.node.ID); err != nil {
			return fmt.Errorf("%s %q: %w", s.node.Kind, s.node.Label, err)
		}
		w.remove(s.node.ID)
	}
	return nil
}

func (w *treeWriter) indexOf(id string) int {
	return slices.Index(w.children[w.parentOf[id]], id)
}

func (w *treeWriter) remove(id string) {
	parent := w.parentOf[id]
	w.children[parent] = slices.DeleteFunc(w.children[parent], func(c string) bool { return c == id })
	delete(w.parentOf, id)
}

// insert mirrors the API's placement: index among the siblings, clamped.
func (w *treeWriter) insert(id, parentID string, index int) {
	list := w.children[parentID]
	index = min(index, len(list))
	w.children[parentID] = slices.Insert(list, index, id)
	w.parentOf[id] = parentID
}

func describeNode(n *planNode) string {
	if n.file != "" {
		return n.file
	}
	return strings.TrimSpace(n.kind + " " + n.label + " " + n.title)
}

func optional(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
