package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/The-LibreTimes/libretimes-cli/internal/api"
	"github.com/The-LibreTimes/libretimes-cli/internal/config"
)

// `lt categories` looks the shared taxonomy up by term or key, so a file can
// name a category by what it is called instead of by an id copied from some
// other environment (TAXONOMY-INPUT T9, #1775). Both reads are public: no
// sign-in, no secret, the same calls the editor makes.
func newCategoriesCommand(e *env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "categories",
		Short: "Find categories by term, and see what a term will become on save",
		Long: "Find categories by term, and see what a term will become on save.\n\n" +
			"A work's `tags` are terms: on save, a term that names a category (its key, " +
			"its name, or an alias) becomes that category, and the rest stay keywords. " +
			"`categories` takes keys, the explicit form. These commands show both " +
			"decisions before you write anything. No sign-in is needed.",
	}
	cmd.AddCommand(newCategoriesSearchCommand(e), newCategoriesResolveCommand(e))
	return cmd
}

func newCategoriesSearchCommand(e *env) *cobra.Command {
	var lang string
	var limit int
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Categories for a partly typed term, best first",
		Long: "Categories for a partly typed term, best first.\n\n" +
			"Loose on purpose -- prefix, word stem, typo, and the Russian/English " +
			"keyboard-layout swap (`fkut,hf` finds Алгебра) -- because it only offers. " +
			"Each hit shows its context (`Linear algebra — Algebra · Mathematics`) and, " +
			"when an alias or another language matched, what matched.",
		Example: "  lt categories search топология\n  lt categories search ML --lang en --json",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := e.client(cmd.Context(), false, config.ProductLibreTimes)
			if err != nil {
				return err
			}
			hits, err := client.SuggestCategories(cmd.Context(), strings.Join(args, " "), lang, limit)
			if err != nil {
				return &exitError{code: ExitAPIError, err: err}
			}
			return e.emit(hits, func(w io.Writer) {
				if len(hits) == 0 {
					fmt.Fprintln(w, "no category matches")
					return
				}
				for _, h := range hits {
					printHit(w, h, "")
				}
			})
		},
	}
	cmd.Flags().StringVar(&lang, "lang", "", "Language for names and context (default: the API's choice from your locale)")
	cmd.Flags().IntVar(&limit, "limit", 8, "At most this many hits (1-25)")
	return cmd
}

func newCategoriesResolveCommand(e *env) *cobra.Command {
	var lang string
	var keys []string
	cmd := &cobra.Command{
		Use:   "resolve <term>...",
		Short: "What each term (or --key) becomes on save: a category or a keyword",
		Long: "What each term becomes on save, decided exactly as the save decides it.\n\n" +
			"A term names one category or none: a key, a name in --lang, an alias in " +
			"--lang, then a name or alias in another language. None makes it a keyword, " +
			"shown with suggestions. --key checks category keys, which a save refuses " +
			"when unknown.",
		Example: "  lt categories resolve \"линейная алгебра\" книги --lang ru\n" +
			"  lt categories resolve --key linear-algebra --key algebre",
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 && len(keys) == 0 {
				return fail(ExitBadArguments, "give at least one term or --key")
			}
			client, _, err := e.client(cmd.Context(), false, config.ProductLibreTimes)
			if err != nil {
				return err
			}
			out, err := client.ResolveTerms(cmd.Context(), api.ResolveTermsInput{
				Lang: lang, Terms: args, Keys: keys, Suggestions: 3,
			})
			if err != nil {
				return &exitError{code: ExitAPIError, err: err}
			}
			return e.emit(out, func(w io.Writer) { printResolution(w, out) })
		},
	}
	cmd.Flags().StringVar(&lang, "lang", "", "The work's language (default: from your locale)")
	cmd.Flags().StringArrayVar(&keys, "key", nil, "A category key to check (repeatable)")
	return cmd
}

func printHit(w io.Writer, h api.CategoryHit, indent string) {
	line := fmt.Sprintf("%s%-28s %s", indent, h.Key, h.Label())
	extra := h.Match
	if h.MatchedText != "" {
		extra += ": " + h.MatchedText
	}
	fmt.Fprintf(w, "%s  (%s)\n", line, extra)
}

func printResolution(w io.Writer, out *api.ResolveTermsOutput) {
	for _, t := range out.Terms {
		switch {
		case t.Category != nil:
			fmt.Fprintf(w, "%q -> category %s  %s  (%s)\n", t.Term, t.Category.Key, t.Category.Label(), t.Category.Match)
		case t.Norm == "":
			fmt.Fprintf(w, "%q -> nothing (empty once tidied)\n", t.Term)
		default:
			note := ""
			if t.Generic {
				note = "  [GENERIC_TAG: too general to help a reader]"
			}
			fmt.Fprintf(w, "%q -> keyword %q%s\n", t.Term, t.Text, note)
			printSuggestions(w, t.Suggestions)
		}
	}
	for _, k := range out.Keys {
		if k.Category != nil {
			fmt.Fprintf(w, "key %s -> %s\n", k.Key, k.Category.Label())
			continue
		}
		fmt.Fprintf(w, "key %s -> no such category\n", k.Key)
		printSuggestions(w, k.Suggestions)
	}
}

func printSuggestions(w io.Writer, hits []api.CategoryHit) {
	if len(hits) == 0 {
		return
	}
	fmt.Fprintln(w, "    did you mean:")
	for _, h := range hits {
		printHit(w, h, "      ")
	}
}
