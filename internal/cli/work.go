package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/The-LibreTimes/libretimes-cli/internal/api"
	"github.com/The-LibreTimes/libretimes-cli/internal/config"
	"github.com/The-LibreTimes/libretimes-cli/internal/frontmatter"
)

// `lt work` is everything published as a work: a lecture, a problem sheet, an
// essay, a book. A book is a type of work, not a separate command -- its file
// is a manifest with `type: book` and a `contents` tree, and `lt work publish`
// routes it to the book reconciler by that type.

func newWorkCommand(e *env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "work",
		Short: "Publish and manage your works: lectures, sheets, notes, books",
	}
	cmd.AddCommand(
		newWorkPublishCommand(e),
		newWorkLsCommand(e),
		newWorkShowCommand(e),
		newWorkEditCommand(e),
		newWorkNotesCommand(e),
		newWorkSuggestCommand(e),
		newWorkReviewCommand(e),
		newWorkDecideCommand(e, "accept"),
		newWorkDecideCommand(e, "reject"),
		newWorkAttachmentsCommand(e),
		newTranslationsCommand(e, workTranslations),
	)
	return cmd
}

func newWorkPublishCommand(e *env) *cobra.Command {
	var dryRun, yes, prune bool

	cmd := &cobra.Command{
		Use:   "publish <file.md>...",
		Short: "Reconcile markdown files as works",
		Long: "Reconcile one or more markdown files as LibreTimes works.\n\n" +
			"The frontmatter is the import manifest: `type` is required, and " +
			"`import_key` is what makes a re-run an update instead of a duplicate. A " +
			"file with `type: book` is a book manifest: its `contents` tree names one " +
			"file per chapter, the manifest is authoritative for their order, and a " +
			"chapter on the site that it no longer names stops the run unless --prune " +
			"is given. Run with --dry-run first — it reports the plan per file and " +
			"writes nothing.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, err := resolveFiles(args)
			if err != nil {
				return err
			}
			if err := needsConfirm(dryRun, yes,
				fmt.Sprintf("publish would write %d work(s)", len(paths))); err != nil {
				return err
			}

			// A token is required even for --dry-run. The plan's whole value
			// is saying which files would be created and which updated, and
			// that answer comes from reading your own works. Without a token
			// the lookup 401s and every file reports an auth failure, which
			// reads as a broken publish rather than a missing credential.
			client, _, err := e.client(cmd.Context(), true, config.ProductLibreTimes)
			if err != nil {
				return err
			}

			results := make([]any, 0, len(paths))
			failed := false
			profiles := newProfileResolver(client)
			for _, path := range paths {
				var action string
				if isBookFile(path) {
					r := publishBook(cmd.Context(), client, path, bookOptions{
						dryRun: dryRun, prune: prune, profiles: profiles,
					})
					action = r.Action
					results = append(results, r)
					if !e.jsonOut {
						renderBook(e, r)
					}
				} else {
					r := fileResult{Path: path}
					if prune {
						r.Action, r.Code = actionFailed, "validation_error"
						r.Message = "--prune is for books; this file is not `type: book`"
					} else {
						r = publishOne(cmd.Context(), client, path, dryRun, profiles)
					}
					action = r.Action
					results = append(results, r)
					if !e.jsonOut {
						renderResult(e, r)
					}
				}
				if action == actionFailed || action == actionBlocked {
					failed = true
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
		"Books only: delete chapters on the site that the manifest no longer lists. On a "+
			"published book their reading numbers are retired, never reissued.")
	return cmd
}

// isBookFile reports whether a file is a book manifest. An unreadable file is
// not one: publishOne then reports the read error with the file's name.
func isBookFile(path string) bool {
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	meta, _, err := frontmatter.Split[frontmatter.Meta](string(raw))
	return err == nil && strings.TrimSpace(meta.Type) == api.TypeBook
}

func newWorkLsCommand(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "ls",
		Short: "List your works, published and draft",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, _, err := e.client(cmd.Context(), true, config.ProductLibreTimes)
			if err != nil {
				return err
			}
			works, err := client.ListMyWorks(cmd.Context())
			if err != nil {
				return err
			}
			return e.emit(works, func(w io.Writer) {
				if len(works) == 0 {
					fmt.Fprintln(w, "  no works yet")
				}
				for _, work := range works {
					fmt.Fprintf(w, "  %-36s %-12s %-8s %s\n", work.ID, work.Type, workState(work), work.Title)
				}
			})
		},
	}
}

// workState is the one word a reader of a listing needs: whether anyone else
// can open it.
func workState(w api.Work) string {
	if w.IsDraft {
		return "draft"
	}
	if w.Visibility == "" {
		return "public"
	}
	return w.Visibility
}

// workShow is what `lt work show --json` emits.
type workShow struct {
	api.Work
	Translations *api.TranslationsOverview `json:"translations,omitempty"`
}

func newWorkShowCommand(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "show <work>",
		Short: "Show one work: its state, languages and files",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := e.client(cmd.Context(), true, config.ProductLibreTimes)
			if err != nil {
				return err
			}
			id, err := resolveWork(cmd.Context(), client, args[0])
			if err != nil {
				return err
			}
			work, err := client.GetWork(cmd.Context(), id)
			if err != nil {
				return err
			}
			out := workShow{Work: *work}
			// Degrade rather than fail: the work itself is what was asked for.
			if overview, err := client.GetTranslations(cmd.Context(), api.TranslateWork, id); err == nil {
				out.Translations = overview
			}
			return e.emit(out, func(w io.Writer) {
				fmt.Fprintf(w, "%s\n", work.Title)
				fmt.Fprintf(w, "  id          %s\n", work.ID)
				fmt.Fprintf(w, "  type        %s\n", work.Type)
				fmt.Fprintf(w, "  state       %s\n", workState(*work))
				fmt.Fprintf(w, "  language    %s\n", work.Language)
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
				for i, a := range work.Attachments {
					label := "            "
					if i == 0 {
						label = "  files     "
					}
					fmt.Fprintf(w, "%s  %s (%s)\n", label, a.OriginalName, humanSize(a.SizeBytes))
				}
			})
		},
	}
}
