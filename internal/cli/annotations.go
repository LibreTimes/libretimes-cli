package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/The-LibreTimes/libretimes-cli/internal/api"
	"github.com/The-LibreTimes/libretimes-cli/internal/config"
)

// Annotations on a work (the platform's EDITOR-REVIEW-PLAN-2026-10-17): read
// what is on it, suggest an edit to somebody else's text, and -- on your own
// work -- review the suggestions and errata and accept or reject them.
//
//	lt work notes   <work>                    what is on it
//	lt work suggest <work> --old … --new …    propose a fix
//	lt work review  <work>                    open suggestions, with their change
//	lt work accept  <work> <id>… -m …         apply them as one version
//	lt work reject  <work> <id>…              turn them down
//
// A passage is named by quoting the work's markdown exactly, which is what the
// web page anchors to too. Nothing is written into the text: a suggestion
// changes it only when the authors accept it, as one new version.

var annotationKinds = []string{"comment", "note", "suggestion", "erratum"}

// placeFlags say which text a passage is in.
type placeFlags struct {
	lang     string
	division string
	page     int
}

func (p *placeFlags) add(cmd *cobra.Command) {
	cmd.Flags().StringVar(&p.lang, "lang", "", "A single document: the language version (default: its original).")
	cmd.Flags().StringVar(&p.division, "division", "", "A book: the division (chapter) id the passage is in.")
	cmd.Flags().IntVar(&p.page, "page", 0, "A book: the reading page that division is on.")
}

// anchorIn reads the text and anchors `quote` in it.
func (p *placeFlags) anchorIn(ctx context.Context, client *api.Client, workID, quote string) (*api.Anchor, error) {
	var source string
	var target api.AnchorTarget
	var version *int
	if p.division != "" {
		if p.page < 1 {
			return nil, fail(ExitBadArguments, "--division needs --page, the reading page it is on")
		}
		text, err := client.GetDivisionText(ctx, workID, p.division, p.page)
		if err != nil {
			return nil, err
		}
		source, target = text, api.AnchorTarget{DivisionID: p.division, Field: "body"}
	} else {
		w, err := client.GetWorkText(ctx, workID, p.lang)
		if err != nil {
			return nil, err
		}
		if w.Kind == "book" {
			return nil, fail(ExitBadArguments, "this is a book: name the passage's --division and --page")
		}
		lang := w.Language
		if w.ContentLanguage != nil && w.ContentLanguage.Served != "" {
			lang = w.ContentLanguage.Served
		}
		source, target, version = w.Content, api.AnchorTarget{Language: lang, Field: "content"}, w.CurrentVersion
	}
	anchor, err := api.AnchorFor(source, quote, target, version)
	if err != nil {
		var apiErr *api.Error
		if errors.As(err, &apiErr) {
			return nil, fail(ExitBadArguments, "%s", apiErr.Message)
		}
		return nil, err
	}
	return anchor, nil
}

func newWorkNotesCommand(e *env) *cobra.Command {
	var kinds, statuses []string
	var lang string
	cmd := &cobra.Command{
		Use:   "notes <work>",
		Short: "List a work's notes, passage comments, suggested edits and errata",
		Long: "List what is annotated on a work, as you may read it: public annotations, " +
			"your own notes, and -- on your own work -- what was sent to the authors.\n\n" +
			"--kind narrows to comment, note, suggestion or erratum; --status to open, " +
			"accepted, rejected or outdated (suggestions and errata).",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, k := range kinds {
				if !contains(annotationKinds, k) {
					return fail(ExitBadArguments, "--kind %q: one of %s", k, strings.Join(annotationKinds, ", "))
				}
			}
			client, _, err := e.client(cmd.Context(), false, config.ProductLibreTimes)
			if err != nil {
				return err
			}
			workID, err := resolveWork(cmd.Context(), client, args[0])
			if err != nil {
				return err
			}
			items, _, err := client.ListAnnotations(cmd.Context(), workID, kinds, statuses, lang)
			if err != nil {
				return err
			}
			items = shown(items)
			return e.emit(items, func(w io.Writer) { printAnnotations(w, items) })
		},
	}
	cmd.Flags().StringSliceVar(&kinds, "kind", nil, "Only these kinds (repeat or comma-separate).")
	cmd.Flags().StringSliceVar(&statuses, "status", nil, "Only suggestions and errata in these states.")
	cmd.Flags().StringVar(&lang, "lang", "", "Only those on this language version of the text.")
	return cmd
}

func newWorkReviewCommand(e *env) *cobra.Command {
	var lang string
	cmd := &cobra.Command{
		Use:   "review <work>",
		Short: "Show the open suggested edits and errata on your work, with their changes",
		Long: "List the suggested edits and errata on a work that are still open, each " +
			"with the passage and -- for a suggestion -- the change as a diff. Decide " +
			"them with `lt work accept` or `lt work reject`.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := e.client(cmd.Context(), true, config.ProductLibreTimes)
			if err != nil {
				return err
			}
			workID, err := resolveWork(cmd.Context(), client, args[0])
			if err != nil {
				return err
			}
			items, _, err := client.ListAnnotations(cmd.Context(), workID,
				[]string{"suggestion", "erratum"}, []string{"open"}, lang)
			if err != nil {
				return err
			}
			items = shown(items)
			return e.emit(items, func(w io.Writer) {
				if len(items) == 0 {
					fmt.Fprintln(w, "nothing open")
					return
				}
				printAnnotations(w, items)
			})
		},
	}
	cmd.Flags().StringVar(&lang, "lang", "", "Only those on this language version of the text.")
	return cmd
}

func newWorkSuggestCommand(e *env) *cobra.Command {
	var oldText, newText, note string
	var yes, authorsOnly bool
	var place placeFlags
	cmd := &cobra.Command{
		Use:   "suggest <work> --old <text> --new <text>",
		Short: "Suggest an edit to a work's text; its authors accept or reject it",
		Long: "Propose replacing --old with --new in a work you can read -- a typo, a " +
			"wrong formula, a clearer sentence. --old is the exact markdown, occurring " +
			"once in the text (quote more around it if it repeats). The authors see it " +
			"beside the passage, and accepting it makes it a new version credited to " +
			"you. Public unless --authors-only. A book's passage needs --division and " +
			"--page.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if oldText == "" || !cmd.Flags().Changed("new") {
				return fail(ExitBadArguments, "--old and --new are required (--new \"\" proposes deleting --old)")
			}
			if oldText == newText {
				return fail(ExitBadArguments, "--new is the same as --old: nothing to suggest")
			}
			if err := needsConfirm(false, yes, "suggest would write a suggestion on the work"); err != nil {
				return err
			}
			client, _, err := e.client(cmd.Context(), true, config.ProductLibreTimes)
			if err != nil {
				return err
			}
			workID, err := resolveWork(cmd.Context(), client, args[0])
			if err != nil {
				return err
			}
			anchor, err := place.anchorIn(cmd.Context(), client, workID, oldText)
			if err != nil {
				return err
			}
			in := api.AnnotationCreate{
				Kind:   "suggestion",
				Anchor: *anchor,
				Change: &api.SuggestedChange{Old: oldText, New: newText},
				Body:   strings.TrimSpace(note),
			}
			if authorsOnly {
				in.Visibility = "authors"
			}
			out, err := client.CreateAnnotation(cmd.Context(), workID, in)
			if err != nil {
				return err
			}
			return e.emit(out, func(w io.Writer) {
				fmt.Fprintf(w, "suggested %s (%s); the authors are told\n", out.ID, out.Visibility)
			})
		},
	}
	cmd.Flags().StringVar(&oldText, "old", "", "The exact markdown to replace, once.")
	cmd.Flags().StringVar(&newText, "new", "", "What should stand there instead.")
	cmd.Flags().StringVar(&note, "note", "", "Why, for the authors.")
	cmd.Flags().BoolVar(&authorsOnly, "authors-only", false, "Send it to the authors only, not shown to readers.")
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm the write.")
	place.add(cmd)
	return cmd
}

func newWorkDecideCommand(e *env, action string) *cobra.Command {
	var message, note string
	var yes bool
	short := map[string]string{
		"accept": "Accept suggested edits (one new version) or mark errata fixed",
		"reject": "Reject suggested edits or errata",
	}[action]
	cmd := &cobra.Command{
		Use:   action + " <work> <id>...",
		Short: short,
		Long: short + ", on a work you may edit. Ids come from `lt work review`.\n\n" +
			"Accepting applies every accepted suggestion as one version (--message is " +
			"its summary; by default it credits the suggesters). One whose passage " +
			"has changed since comes back outdated and the rest still apply. The " +
			"writers are told.",
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := needsConfirm(false, yes, action+" would decide "+fmt.Sprint(len(args)-1)+" annotation(s)"); err != nil {
				return err
			}
			client, _, err := e.client(cmd.Context(), true, config.ProductLibreTimes)
			if err != nil {
				return err
			}
			workID, err := resolveWork(cmd.Context(), client, args[0])
			if err != nil {
				return err
			}
			out, err := client.ResolveAnnotations(cmd.Context(), workID, args[1:], action, message, note)
			if err != nil {
				return err
			}
			if err := e.emit(out, func(w io.Writer) { printResolved(w, out) }); err != nil {
				return err
			}
			for _, r := range out.Results {
				if !r.OK {
					return &exitError{code: ExitAPIError, err: fmt.Errorf("%s: not decided (%s)", r.ID, r.Error)}
				}
			}
			return nil
		},
	}
	if action == "accept" {
		cmd.Flags().StringVarP(&message, "message", "m", "", "The new version's summary.")
	}
	cmd.Flags().StringVar(&note, "note", "", "A word to the writers.")
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm the decision.")
	return cmd
}

// shown drops a deleted annotation with no replies: nothing is left to read.
func shown(items []api.Annotation) []api.Annotation {
	out := items[:0:0]
	for _, a := range items {
		if !a.IsDeleted || a.Replies > 0 {
			out = append(out, a)
		}
	}
	return out
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// oneLine shortens text for a listing.
func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func printAnnotations(w io.Writer, items []api.Annotation) {
	if len(items) == 0 {
		fmt.Fprintln(w, "no annotations")
		return
	}
	for _, a := range items {
		who := "?"
		if a.Author != nil && a.Author.Username != "" {
			who = "@" + a.Author.Username
		}
		head := a.Kind
		if a.Status != "" {
			head += " · " + a.Status
		}
		if a.Visibility != "public" {
			head += " · " + a.Visibility
		}
		fmt.Fprintf(w, "%s  %s  %s\n", a.ID, head, who)
		if a.Anchor != nil {
			fmt.Fprintf(w, "  on: %q\n", oneLine(a.Anchor.Quote.Exact, 120))
		}
		if a.Change != nil {
			for _, line := range strings.Split(a.Change.Old, "\n") {
				fmt.Fprintf(w, "  - %s\n", line)
			}
			for _, line := range strings.Split(a.Change.New, "\n") {
				fmt.Fprintf(w, "  + %s\n", line)
			}
		}
		if a.Body != "" {
			fmt.Fprintf(w, "  %s\n", oneLine(a.Body, 200))
		}
	}
}

func printResolved(w io.Writer, r *api.Resolved) {
	for _, x := range r.Results {
		if x.OK {
			fmt.Fprintf(w, "%s  %s\n", x.ID, x.Status)
		} else {
			fmt.Fprintf(w, "%s  not decided: %s\n", x.ID, x.Error)
		}
	}
	if r.VersionNumber != nil {
		fmt.Fprintf(w, "committed as version %d\n", *r.VersionNumber)
	}
	if r.AwaitingPublish {
		fmt.Fprintln(w, "a published record: the new version waits to be published as a corrigendum")
	}
}
