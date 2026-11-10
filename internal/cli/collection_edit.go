package cli

import (
	"context"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/The-LibreTimes/libretimes-cli/internal/api"
	"github.com/The-LibreTimes/libretimes-cli/internal/config"
	"github.com/The-LibreTimes/libretimes-cli/internal/frontmatter"
)

// The one-command forms of `lt collection`: make a list, then add and remove
// works by identifier, without writing a manifest. Every write takes --yes
// (or --dry-run), as everywhere else in lt.

const (
	itemRemove = "remove" // rm: the work is in the collection and is taken out
	itemAbsent = "absent" // rm: the work is not in the collection; nothing is sent
)

// editResult is what `add` and `rm` report.
type editResult struct {
	CollectionID string         `json:"collection_id"`
	Section      *sectionResult `json:"section,omitempty"`
	Items        []itemResult   `json:"items"`
}

func newCollectionCreateCommand(e *env) *cobra.Command {
	var kind, title, language, description, visibility, importKey string
	var categories []string
	var dryRun, yes bool

	cmd := &cobra.Command{
		Use:   "create --kind <kind> --title <title>",
		Short: "Create an empty collection or reading list",
		Long: "Create a collection, then fill it with `lt collection add`.\n\n" +
			"  lt collection create --kind reading_list --title 'Algebra' --language en --yes\n\n" +
			"--kind is one of " + strings.Join(api.CollectionTypeValues, ", ") + ". " +
			"--category takes a category key (`lt categories` lists them) and may be repeated. " +
			"With --import-key, a later `lt collection publish` of a manifest with the same key " +
			"updates this collection instead of making another.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			kind, title = strings.TrimSpace(kind), strings.TrimSpace(title)
			switch {
			case !slices.Contains(api.CollectionTypeValues, kind):
				return fail(ExitBadArguments, "--kind %q is not one of %s", kind, strings.Join(api.CollectionTypeValues, ", "))
			case title == "":
				return fail(ExitBadArguments, "--title is required")
			case len(title) > 255:
				return fail(ExitBadArguments, "--title is at most 255 characters")
			case visibility != "" && !slices.Contains(api.CollectionVisibilityValues, visibility):
				return fail(ExitBadArguments, "--visibility %q is not one of %s", visibility,
					strings.Join(api.CollectionVisibilityValues, ", "))
			}
			in := api.CollectionInput{
				Title: title, Description: description, PrimaryLanguage: language,
				Visibility: visibility, CollectionType: kind, Categories: categories,
				ImportKey: strings.TrimSpace(importKey),
			}
			if err := needsConfirm(dryRun, yes, fmt.Sprintf("collection create would make the %s %q", kind, title)); err != nil {
				return err
			}
			client, _, err := e.client(cmd.Context(), true, config.ProductLibreTimes)
			if err != nil {
				return err
			}
			if in.ImportKey != "" {
				held, err := client.FindCollectionByImportKey(cmd.Context(), in.ImportKey)
				if err != nil {
					return err
				}
				if held != nil {
					return &api.Error{Code: "conflict", Message: fmt.Sprintf(
						"your collection %s (%q) already carries the import_key %q", held.ID, held.Title, in.ImportKey)}
				}
			}
			out := map[string]any{"action": actionCreate, "collection_type": kind, "title": title}
			if dryRun {
				return e.emit(out, func(w io.Writer) { fmt.Fprintf(w, "  create   %s %q\n", kind, title) })
			}
			created, err := client.CreateCollection(cmd.Context(), in)
			if err != nil {
				return err
			}
			out["collection_id"] = created.ID
			if created.ImportKey != "" {
				out["import_key"] = created.ImportKey
			}
			if len(created.Terms) > 0 {
				out["terms"] = created.Terms
			}
			return e.emit(out, func(w io.Writer) {
				fmt.Fprintf(w, "  create   %s %q %s\n", kind, title, created.ID)
				renderTerms(w, created.Terms)
			})
		},
	}
	cmd.Flags().StringVar(&kind, "kind", "", "What it is: "+strings.Join(api.CollectionTypeValues, ", ")+" (required).")
	cmd.Flags().StringVar(&title, "title", "", "The title (required).")
	cmd.Flags().StringVar(&language, "language", "en", "The language the title and description are written in.")
	cmd.Flags().StringVar(&description, "description", "", "What it is, in a sentence or two.")
	cmd.Flags().StringArrayVar(&categories, "category", nil, "A category key, e.g. linear-algebra (repeatable).")
	cmd.Flags().StringVar(&visibility, "visibility", "", "Who may see it: "+strings.Join(api.CollectionVisibilityValues, ", ")+".")
	cmd.Flags().StringVar(&importKey, "import-key", "", "A key a manifest can later name it by, e.g. <you>/<list>.")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show the plan; write nothing.")
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm the write.")
	return cmd
}

// collectionState is one collection's sections and items, read once.
type collectionState struct {
	id       string
	sections []api.CollectionSection
	items    []api.CollectionItem
	byWork   map[string]api.CollectionItem
}

func readCollection(ctx context.Context, client *api.Client, id string) (*collectionState, error) {
	coll, err := client.GetCollection(ctx, id)
	if err != nil {
		return nil, err
	}
	items, err := client.ListCollectionItems(ctx, id)
	if err != nil {
		return nil, err
	}
	st := &collectionState{id: id, sections: coll.Sections, items: items, byWork: map[string]api.CollectionItem{}}
	for _, it := range items {
		st.byWork[strings.ToLower(it.EntityID)] = it
	}
	return st, nil
}

// sectionByTitle finds a section by its title, as a manifest re-run does.
func (st *collectionState) sectionByTitle(title string) *api.CollectionSection {
	for i := range st.sections {
		if strings.TrimSpace(st.sections[i].Title) == title {
			return &st.sections[i]
		}
	}
	return nil
}

// resolveArgs resolves command-line items the way a manifest's are, files
// relative to the working directory.
func resolveArgs(ctx context.Context, client *api.Client, args []string, note, edition string) ([]itemResult, error) {
	entries := make([]manifestEntry, 0, len(args))
	for _, a := range args {
		entries = append(entries, manifestEntry{frontmatter.CollectionItem{Work: strings.TrimSpace(a), Note: note, Edition: edition}, -1})
	}
	// Any name in the working directory: files resolve relative to it.
	return resolveItems(ctx, client, "./args", entries)
}

func newCollectionAddCommand(e *env) *cobra.Command {
	var edition, section, note string
	var dryRun, yes bool

	cmd := &cobra.Command{
		Use:   "add <collection> <item>...",
		Short: "Add works to a collection by id, DOI, ISBN, arXiv id or catalogue key",
		Long: "Add works the site already holds to a collection, at the end of the list or of " +
			"--section (made if it does not exist).\n\n" +
			"  lt collection add <collection> 978-0-387-90244-9 10.1007/978-3-642-29514-0 \\\n" +
			"      book:fulton-intersection-theory --section 'Further reading' --yes\n\n" +
			"An item is a work id or URL, a DOI, an arXiv id, an ISBN (which pins that edition), a " +
			"library catalogue key (book:<key>) or a work file. --edition pins an edition by its " +
			"ISBN, <language>-<year> (ru-1976) or id; --edition and --note need a single item. " +
			"A work already in the collection gets the note, edition and section given. If any " +
			"item names no work the site holds, nothing is written: lt never creates a work.\n\n" +
			"<collection> is an id, a /collections/<id> URL or a manifest file.",
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			items := args[1:]
			if (edition != "" || note != "") && len(items) > 1 {
				return fail(ExitBadArguments, "--edition and --note apply to one item; give them one at a time")
			}
			if len(note) > 2000 {
				return fail(ExitBadArguments, "--note is at most 2000 characters")
			}
			section = strings.TrimSpace(section)
			if len(section) > 255 {
				return fail(ExitBadArguments, "--section is at most 255 characters")
			}
			if err := needsConfirm(dryRun, yes, fmt.Sprintf("collection add would add %d item(s)", len(items))); err != nil {
				return err
			}
			client, _, err := e.client(cmd.Context(), true, config.ProductLibreTimes)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			id, err := resolveCollection(ctx, client, args[0])
			if err != nil {
				return err
			}
			resolved, err := resolveArgs(ctx, client, items, note, edition)
			if err != nil {
				return fail(ExitBadArguments, "%v", err)
			}
			st, err := readCollection(ctx, client, id)
			if err != nil {
				return err
			}
			result := editResult{CollectionID: id, Items: resolved}

			var target *api.CollectionSection
			if section != "" {
				target = st.sectionByTitle(section)
				result.Section = &sectionResult{Title: section, Action: actionCreate}
				if target != nil {
					result.Section.Action, result.Section.SectionID = itemPresent, strings.ToLower(target.ID)
				}
			}
			skipped := false
			moves := map[string]bool{}
			for i := range result.Items {
				r := &result.Items[i]
				r.Edition = r.pin
				if r.Action == itemSkip {
					skipped = true
					continue
				}
				r.Section = section
				have, ok := st.byWork[r.PublicationID]
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
				if section != "" && (target == nil || !strings.EqualFold(target.ID, have.SectionID)) {
					r.Changes = append(r.Changes, "section")
					moves[r.PublicationID] = true
				}
				if len(r.Changes) > 0 {
					r.Action = itemUpdate
				}
			}
			if skipped {
				_ = e.emit(result, func(w io.Writer) { renderEdit(e, result) })
				return &api.Error{Code: "not_found", Message: "an item names no work the site holds " +
					"(see its reason); nothing was written. lt never creates a work"}
			}
			if dryRun {
				return e.emit(result, func(io.Writer) { renderEdit(e, result) })
			}

			sectionID := ""
			if target != nil {
				sectionID = strings.ToLower(target.ID)
			} else if section != "" {
				made, err := client.CreateSection(ctx, id, api.SectionInput{Title: section})
				if err != nil {
					return err
				}
				sectionID = strings.ToLower(made.ID)
				result.Section.SectionID = sectionID
				st.sections = append(st.sections, *made)
			}
			var adds []api.ItemIn
			for _, r := range result.Items {
				if r.Action == itemAdd {
					adds = append(adds, api.ItemIn{EntityID: r.PublicationID, Note: r.note, ExpressionID: r.pin, SectionID: sectionID})
				}
			}
			if len(adds) > 0 {
				if err := client.AddWorksToCollection(ctx, id, adds); err != nil {
					return err
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
				if len(fields) > 0 {
					if err := client.UpdateCollectionItem(ctx, id, r.PublicationID, fields); err != nil {
						return err
					}
				}
			}
			if len(moves) > 0 {
				if err := moveToSection(ctx, client, st, moves, sectionID); err != nil {
					return err
				}
			}
			return e.emit(result, func(io.Writer) { renderEdit(e, result) })
		},
	}
	cmd.Flags().StringVar(&edition, "edition", "", "Pin an edition: its ISBN, <language>-<year> (ru-1976) or id. One item only.")
	cmd.Flags().StringVar(&section, "section", "", "Put the items under this heading, made if missing.")
	cmd.Flags().StringVar(&note, "note", "", "A line or two under the item. One item only.")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show the plan; write nothing.")
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm the writes.")
	return cmd
}

// moveToSection puts the moved works at the end of the target section (""
// is the unsectioned preamble), every other item staying where it is.
func moveToSection(ctx context.Context, client *api.Client, st *collectionState, moves map[string]bool, target string) error {
	sections := slices.Clone(st.sections)
	sort.SliceStable(sections, func(i, j int) bool { return sections[i].Position < sections[j].Position })
	items := slices.Clone(st.items)
	sort.SliceStable(items, func(i, j int) bool { return items[i].Position < items[j].Position })

	keys := []string{""}
	for _, s := range sections {
		keys = append(keys, strings.ToLower(s.ID))
	}
	groups := map[string][]string{}
	var moved []string
	for _, it := range items {
		wid := strings.ToLower(it.EntityID)
		if moves[wid] {
			moved = append(moved, wid)
			continue
		}
		key := strings.ToLower(it.SectionID)
		groups[key] = append(groups[key], wid)
	}
	groups[target] = append(groups[target], moved...)

	var body []api.OrderGroup
	for _, key := range keys {
		g := api.OrderGroup{EntityIDs: append([]string{}, groups[key]...)}
		if key != "" {
			k := key
			g.SectionID = &k
		} else if len(g.EntityIDs) == 0 {
			continue
		}
		body = append(body, g)
	}
	return client.ReorderCollectionSections(ctx, st.id, body)
}

func renderEdit(e *env, r editResult) {
	if r.Section != nil {
		fmt.Fprintf(e.out, "    section  %-8s %s\n", r.Section.Action, r.Section.Title)
	}
	for _, l := range r.Items {
		trailing := l.PublicationID
		if l.Title != "" {
			trailing += "  " + l.Title
		}
		if l.Action == itemSkip {
			trailing = l.Reason
		} else if len(l.Changes) > 0 {
			trailing += " (" + strings.Join(l.Changes, ", ") + ")"
		}
		fmt.Fprintf(e.out, "    %-8s %-38s %s\n", l.Action, l.Path, trailing)
	}
}

func newCollectionRmCommand(e *env) *cobra.Command {
	var dryRun, yes bool
	cmd := &cobra.Command{
		Use:   "rm <collection> <item>...",
		Short: "Take works out of a collection",
		Long: "Take works out of a collection. The works themselves are untouched. An item is named " +
			"as for `lt collection add`; one that is not in the collection is reported `absent`.",
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := needsConfirm(dryRun, yes, fmt.Sprintf("collection rm would remove %d item(s)", len(args)-1)); err != nil {
				return err
			}
			client, _, err := e.client(cmd.Context(), true, config.ProductLibreTimes)
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			id, err := resolveCollection(ctx, client, args[0])
			if err != nil {
				return err
			}
			resolved, err := resolveArgs(ctx, client, args[1:], "", "")
			if err != nil {
				return fail(ExitBadArguments, "%v", err)
			}
			st, err := readCollection(ctx, client, id)
			if err != nil {
				return err
			}
			result := editResult{CollectionID: id, Items: resolved}
			for i := range result.Items {
				r := &result.Items[i]
				// A work deleted or returned to draft since it was added can
				// still be taken out: its id is known.
				if _, held := st.byWork[r.PublicationID]; r.PublicationID != "" && held {
					r.Action, r.Reason = itemRemove, ""
				} else if r.Action != itemSkip {
					r.Action = itemAbsent
				}
			}
			if !dryRun {
				for _, r := range result.Items {
					if r.Action == itemRemove {
						if err := client.RemoveCollectionItem(ctx, id, r.PublicationID); err != nil {
							return err
						}
					}
				}
			}
			return e.emit(result, func(io.Writer) { renderEdit(e, result) })
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show the plan; write nothing.")
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm the writes.")
	return cmd
}

func newCollectionDeleteCommand(e *env) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "delete <collection>",
		Short: "Delete a collection (its works are untouched)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := needsConfirm(false, yes, "collection delete would delete "+args[0]); err != nil {
				return err
			}
			client, _, err := e.client(cmd.Context(), true, config.ProductLibreTimes)
			if err != nil {
				return err
			}
			id, err := resolveCollection(cmd.Context(), client, args[0])
			if err != nil {
				return err
			}
			if err := client.DeleteCollection(cmd.Context(), id); err != nil {
				return err
			}
			return e.emit(map[string]string{"action": "delete", "collection_id": id},
				func(w io.Writer) { fmt.Fprintf(w, "  delete   %s\n", id) })
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm the delete.")
	return cmd
}

func newCollectionSectionCommand(e *env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "section",
		Short: "Add, rename or remove a collection's headings",
	}
	var description string
	var dryRun, yes bool
	flags := func(c *cobra.Command) {
		c.Flags().BoolVar(&dryRun, "dry-run", false, "Show the plan; write nothing.")
		c.Flags().BoolVar(&yes, "yes", false, "Confirm the write.")
	}
	// run reads the collection and hands the section named by title (nil when
	// there is none) to the write.
	run := func(cmd *cobra.Command, coll, title, what string, write func(ctx context.Context, client *api.Client, id string, sec *api.CollectionSection) (string, error)) error {
		title = strings.TrimSpace(title)
		if title == "" || len(title) > 255 {
			return fail(ExitBadArguments, "a section title is 1 to 255 characters")
		}
		if err := needsConfirm(dryRun, yes, "collection section "+what+" would change "+coll); err != nil {
			return err
		}
		client, _, err := e.client(cmd.Context(), true, config.ProductLibreTimes)
		if err != nil {
			return err
		}
		id, err := resolveCollection(cmd.Context(), client, coll)
		if err != nil {
			return err
		}
		st, err := readCollection(cmd.Context(), client, id)
		if err != nil {
			return err
		}
		sec := st.sectionByTitle(title)
		action, err := write(cmd.Context(), client, id, sec)
		if err != nil {
			return err
		}
		out := sectionResult{Title: title, Action: action}
		if sec != nil {
			out.SectionID = strings.ToLower(sec.ID)
		}
		return e.emit(out, func(w io.Writer) { fmt.Fprintf(w, "    section  %-8s %s\n", action, title) })
	}

	add := &cobra.Command{
		Use:   "add <collection> <title>",
		Short: "Append a heading (present if it exists)",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(cmd, args[0], args[1], "add", func(ctx context.Context, client *api.Client, id string, sec *api.CollectionSection) (string, error) {
				if sec != nil {
					if description == "" || description == sec.Description {
						return itemPresent, nil
					}
					if !dryRun {
						return actionUpdate, client.UpdateSection(ctx, id, sec.ID, api.SectionInput{Description: description})
					}
					return actionUpdate, nil
				}
				if !dryRun {
					if _, err := client.CreateSection(ctx, id, api.SectionInput{Title: strings.TrimSpace(args[1]), Description: description}); err != nil {
						return "", err
					}
				}
				return actionCreate, nil
			})
		},
	}
	add.Flags().StringVar(&description, "description", "", "A line under the heading.")
	flags(add)

	rename := &cobra.Command{
		Use:   "rename <collection> <title> <new-title>",
		Short: "Rename a heading",
		Args:  cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			to := strings.TrimSpace(args[2])
			if to == "" || len(to) > 255 {
				return fail(ExitBadArguments, "a section title is 1 to 255 characters")
			}
			return run(cmd, args[0], args[1], "rename", func(ctx context.Context, client *api.Client, id string, sec *api.CollectionSection) (string, error) {
				if sec == nil {
					return "", &api.Error{Code: "not_found", Message: fmt.Sprintf("no section is titled %q", strings.TrimSpace(args[1]))}
				}
				if !dryRun {
					return "rename", client.UpdateSection(ctx, id, sec.ID, api.SectionInput{Title: to})
				}
				return "rename", nil
			})
		},
	}
	flags(rename)

	rm := &cobra.Command{
		Use:   "rm <collection> <title>",
		Short: "Remove a heading; its works move to the top of the list",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(cmd, args[0], args[1], "rm", func(ctx context.Context, client *api.Client, id string, sec *api.CollectionSection) (string, error) {
				if sec == nil {
					return itemAbsent, nil
				}
				if !dryRun {
					return "delete", client.DeleteSection(ctx, id, sec.ID)
				}
				return "delete", nil
			})
		},
	}
	flags(rm)

	cmd.AddCommand(add, rename, rm)
	return cmd
}
