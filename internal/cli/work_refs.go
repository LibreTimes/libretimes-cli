package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/The-LibreTimes/libretimes-cli/internal/api"
	"github.com/The-LibreTimes/libretimes-cli/internal/config"
)

// `lt work refs` reads and edits what a work cites (REFERENCES-PLAN R11):
// the same list the work page shows, formatted by the server in the work's
// style or one asked for. A pure client of the public API.
func newWorkRefsCommand(e *env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "refs",
		Short: "What a work cites: list it in a citation style",
		Long: "What a work cites. `list` prints the work's references formatted by the server, " +
			"in the style the work chose (or --style), with the terms of the text's language " +
			"(or --lang). `styles` lists the styles.",
	}
	cmd.AddCommand(newWorkRefsListCommand(e), newWorkRefsStylesCommand(e))
	return cmd
}

func newWorkRefsListCommand(e *env) *cobra.Command {
	var version int
	var lang, style string
	cmd := &cobra.Command{
		Use:   "list <work>",
		Short: "A work's references, formatted",
		Long: "A work's references, formatted on the server. Each line starts with the " +
			"reference's key, what `[@key]` in the text names.\n\n" +
			"<work> is an id, a /works/<id> URL, or a file you published. --version reads that " +
			"version's list. A public work needs no sign-in.",
		Example: "  lt work refs list 01a1c230-3e5f-7551-8c0d-42b20a8fcd11\n" +
			"  lt work refs list notes.md --style apa --json",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if version < 0 {
				return fail(ExitBadArguments, "--version is a version number, 1 or more")
			}
			client, _, err := e.client(cmd.Context(), false, config.ProductLibreTimes)
			if err != nil {
				return err
			}
			id, err := resolveWork(cmd.Context(), client, args[0])
			if err != nil {
				return err
			}
			bib, err := client.GetBibliography(cmd.Context(), id, api.BibliographyQuery{
				Version: version, Lang: lang, Style: style,
			})
			if err != nil {
				return &exitError{code: ExitAPIError, err: err}
			}
			return e.emit(bib, func(w io.Writer) {
				if len(bib.Entries) == 0 {
					fmt.Fprintln(w, "no references")
					return
				}
				fmt.Fprintf(w, "# %s, %s\n", bib.Style, bib.Lang)
				for _, entry := range bib.Entries {
					fmt.Fprintf(w, "[%s] %s\n", entry.ID, entry.Text)
				}
			})
		},
	}
	cmd.Flags().IntVar(&version, "version", 0, "Read this version's list (default: the one a reader is served).")
	cmd.Flags().StringVar(&lang, "lang", "", "Language of the terms (default: the text's).")
	cmd.Flags().StringVar(&style, "style", "", "Citation style (default: the work's own; see `lt work refs styles`).")
	return cmd
}

func newWorkRefsStylesCommand(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "styles",
		Short: "The citation styles the server formats in",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := e.client(cmd.Context(), false, config.ProductLibreTimes)
			if err != nil {
				return err
			}
			styles, err := client.ListCitationStyles(cmd.Context())
			if err != nil {
				return &exitError{code: ExitAPIError, err: err}
			}
			return e.emit(styles, func(w io.Writer) {
				for _, s := range styles {
					mark := ""
					if s.Default {
						mark = " (default)"
					}
					fmt.Fprintf(w, "%-28s %s, %s%s\n", s.ID, s.Title, s.Kind, mark)
				}
			})
		},
	}
}
