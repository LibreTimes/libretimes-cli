package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/The-LibreTimes/libretimes-cli/internal/api"
	"github.com/The-LibreTimes/libretimes-cli/internal/config"
)

// recordResult is what `lt work record --json` emits.
type recordResult struct {
	Action    string   `json:"action"` // create | exists | dry_run
	WorkID    string   `json:"work_id,omitempty"`
	Title     string   `json:"title"`
	Type      string   `json:"type"`
	MatchedBy string   `json:"matched_by,omitempty"`
	Problems  []string `json:"problems,omitempty"`
}

func newWorkRecordCommand(e *env) *cobra.Command {
	var dryRun, yes bool

	cmd := &cobra.Command{
		Use:   "record <record.yaml>",
		Short: "Add a work you did not write, such as a book for a reading list",
		Long: "Record a book, article or other work that is not yours so it can be listed, cited " +
			"and put in a reading list. You appear as \"added by\", never as an author; names are " +
			"as the book prints them; every edition is listed only, metadata and a link, never " +
			"text. A DOI, ISBN (an edition's or a tome's), arXiv id, PMID, OpenAlex id or Wikidata " +
			"id the catalogue already holds answers with that record instead of a copy, so a " +
			"re-run is safe.\n\n" +
			"The file is YAML or JSON in the public API's own shape (POST /works/records): `type`, " +
			"`title` and `language` are required; a book lists `editions` (language, label, " +
			"publisher, year, isbn, page_count, series_title, series_number, volumes, coverage, " +
			"source_url) and `names` (display_name, role). `-` reads standard input.",
		Example: "  type: book\n  title: Introduction to Matrix Analysis\n  language: en\n" +
			"  first_published: \"1960\"\n  names:\n    - {display_name: Richard Bellman, role: author}\n" +
			"  editions:\n    - {language: en, publisher: McGraw-Hill, year: 1960, isbn: \"9780898713992\"}",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := readRecordFile(args[0], cmd.InOrStdin())
			if err != nil {
				return err
			}
			if err := needsConfirm(dryRun, yes, "record would add a work to the catalogue"); err != nil {
				return err
			}
			client, _, err := e.client(cmd.Context(), true, config.ProductLibreTimes)
			if err != nil {
				return err
			}
			result, err := recordWork(cmd.Context(), client, body, dryRun)
			if err != nil {
				return err
			}
			return e.emit(result, func(w io.Writer) {
				switch result.Action {
				case "create":
					fmt.Fprintf(w, "  recorded   %s  %s\n", result.WorkID, result.Title)
				case "exists":
					fmt.Fprintf(w, "  exists     %s  %s (matched by %s)\n", result.WorkID, result.Title, result.MatchedBy)
				default:
					fmt.Fprintf(w, "  would record  %s  %s\n", result.Type, result.Title)
				}
			})
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Check the file; send nothing.")
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm the write.")
	return cmd
}

// readRecordFile parses the file (YAML, which includes JSON) into the request
// body and checks what the server would refuse first, in words.
func readRecordFile(path string, stdin io.Reader) (map[string]any, error) {
	var raw []byte
	var err error
	if path == "-" {
		raw, err = io.ReadAll(stdin)
	} else {
		raw, err = os.ReadFile(path)
	}
	if err != nil {
		return nil, fail(ExitBadArguments, "%s: %v", path, err)
	}
	var body map[string]any
	if err := yaml.Unmarshal(raw, &body); err != nil {
		return nil, fail(ExitBadArguments, "%s: %v", path, err)
	}
	if problems := recordProblems(body); len(problems) > 0 {
		return nil, fail(ExitBadArguments, "%s: %s", path, strings.Join(problems, "; "))
	}
	return body, nil
}

// recordProblems are the gaps the server would answer 422 for. The server
// stays the judge of everything else.
func recordProblems(body map[string]any) []string {
	var problems []string
	for _, key := range []string{"type", "title", "language"} {
		if s, _ := body[key].(string); strings.TrimSpace(s) == "" {
			problems = append(problems, fmt.Sprintf("`%s` is required", key))
		}
	}
	return problems
}

func recordWork(ctx context.Context, client *api.Client, body map[string]any, dryRun bool) (recordResult, error) {
	title, _ := body["title"].(string)
	kind, _ := body["type"].(string)
	if dryRun {
		return recordResult{Action: "dry_run", Title: title, Type: kind}, nil
	}
	out, err := client.RecordWork(ctx, body)
	if err != nil {
		return recordResult{}, err
	}
	action := "create"
	if !out.Created {
		action = "exists"
	}
	return recordResult{Action: action, WorkID: out.Work.ID, Title: out.Work.Title,
		Type: out.Work.Type, MatchedBy: out.MatchedBy}, nil
}
