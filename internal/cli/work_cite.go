package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/The-LibreTimes/libretimes-cli/internal/api"
	"github.com/The-LibreTimes/libretimes-cli/internal/config"
)

func newWorkCiteCommand(e *env) *cobra.Command {
	var edition, lang, format, output, style string
	var volume int

	cmd := &cobra.Command{
		Use:   "cite <work>",
		Short: "Write a work's citation as CSL-JSON, or in a citation style",
		Long: "Write the citation of a work, of one edition of it (--edition), or of one " +
			"tome of that edition (--volume), as a CSL-JSON item. The platform builds it from " +
			"the edition -- publisher, year, ISBN, series, volume -- so a book cites the printing " +
			"you name, not a guess. Every citation tool reads CSL-JSON; for BibTeX use pandoc:\n\n" +
			"  lt work cite <work> | pandoc -f csljson -t biblatex\n\n" +
			"<work> is an id, a /works/<id> URL, or a file you published. A public work needs no " +
			"sign-in. With no --edition the edition is the one a reader in --lang would be shown.\n\n" +
			"--style formats it instead, on the server (apa, chicago-author-date, " +
			"gost-r-7-0-5-2008-numeric, ieee; `lt work refs styles` lists them), with the terms " +
			"of --lang:\n\n  lt work cite <work> --style gost-r-7-0-5-2008-numeric --lang ru",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if format != "csl-json" {
				return fail(ExitBadArguments, "--format %q is not supported; the only format is csl-json "+
					"(convert it with pandoc for BibTeX)", format)
			}
			q := api.CitationQuery{Expression: edition, Lang: lang}
			if cmd.Flags().Changed("volume") {
				if edition == "" {
					return fail(ExitBadArguments, "--volume names a tome of one edition; give --edition too")
				}
				if volume < 0 {
					return fail(ExitBadArguments, "--volume is a position, 0 or more")
				}
				q.Volume = &volume
			}
			if edition != "" && !uuidPattern.MatchString(edition) {
				return fail(ExitBadArguments, "--edition %q is not an edition id", edition)
			}
			client, _, err := e.client(cmd.Context(), false, config.ProductLibreTimes)
			if err != nil {
				return err
			}
			id, err := resolveWork(cmd.Context(), client, args[0])
			if err != nil {
				return err
			}
			var data []byte
			if style != "" {
				data, err = citeWorkStyled(cmd.Context(), client, id, q, style)
			} else {
				data, err = citeWork(cmd.Context(), client, id, q)
			}
			if err != nil {
				return err
			}
			if output != "" && output != "-" {
				if err := os.WriteFile(output, data, 0o644); err != nil {
					return fail(ExitBadArguments, "%s: %v", output, err)
				}
				if !e.quiet {
					fmt.Fprintf(e.err, "wrote %s\n", output)
				}
				return nil
			}
			_, err = e.out.Write(data)
			return err
		},
	}
	cmd.Flags().StringVar(&edition, "edition", "", "Cite this edition (an expression id).")
	cmd.Flags().IntVar(&volume, "volume", 0, "Cite one tome of --edition, by its position.")
	cmd.Flags().StringVar(&lang, "lang", "", "Language the work is read in (decides the edition when --edition is absent).")
	cmd.Flags().StringVar(&format, "format", "csl-json", "Output format: csl-json.")
	cmd.Flags().StringVar(&style, "style", "", "Format in this citation style instead of CSL-JSON (the server formats it).")
	cmd.Flags().StringVarP(&output, "output", "o", "", "Write to this file instead of stdout.")
	return cmd
}

// citeWork reads the citation and returns its CSL item, indented, as a
// one-entry array: the same shape `lt collection export` writes, so the two
// feed one pipeline.
func citeWork(ctx context.Context, client *api.Client, workID string, q api.CitationQuery) ([]byte, error) {
	cite, err := client.GetCitation(ctx, workID, q)
	if err != nil {
		return nil, err
	}
	wrapped := append(append([]byte("["), cite.CSL...), ']')
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, wrapped, "", "  "); err != nil {
		pretty.Reset()
		pretty.Write(wrapped)
	}
	pretty.WriteByte('\n')
	return pretty.Bytes(), nil
}

// citeWorkStyled formats the work's citation in one style on the server: the
// same CSL-JSON item `citeWork` writes, posted to /citations/format. Plain
// text, one line.
func citeWorkStyled(ctx context.Context, client *api.Client, workID string, q api.CitationQuery, style string) ([]byte, error) {
	cite, err := client.GetCitation(ctx, workID, q)
	if err != nil {
		return nil, err
	}
	out, err := client.FormatCitations(ctx, []json.RawMessage{cite.CSL}, style, q.Lang)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	for _, entry := range out.Entries {
		buf.WriteString(entry.Text)
		buf.WriteByte('\n')
	}
	return buf.Bytes(), nil
}
