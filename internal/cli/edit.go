package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/The-LibreTimes/libretimes-cli/internal/api"
	"github.com/The-LibreTimes/libretimes-cli/internal/config"
)

// `lt work edit` changes part of a work without a local copy of it: a file of
// exact {old, new} replacements goes into an edit session on the platform,
// the diff is shown, and the session is committed as one version with a
// summary. A script can fix a typo in a published text this way, and a stale
// edit -- someone changed the same text meanwhile -- is refused by the
// platform rather than overwriting them.

// editFile is a changes file: either a bare list of changes, or an object
// carrying `changes` and, for a book, `structure`.
type editFile struct {
	Changes   []json.RawMessage `json:"changes"`
	Structure []json.RawMessage `json:"structure"`
}

func readEditFile(path string) (*editFile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fail(ExitBadArguments, "%s: %v", path, err)
	}
	var out editFile
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &out.Changes); err != nil {
			return nil, fail(ExitBadArguments, "%s: %v", path, err)
		}
	} else if err := json.Unmarshal(trimmed, &out); err != nil {
		return nil, fail(ExitBadArguments, "%s: %v", path, err)
	}
	if len(out.Changes) == 0 && len(out.Structure) == 0 {
		return nil, fail(ExitBadArguments, "%s: no `changes` and no `structure`", path)
	}
	return &out, nil
}

// editReport is what `lt work edit --json` emits.
type editReport struct {
	WorkID          string           `json:"work_id"`
	SessionID       string           `json:"session_id"`
	Action          string           `json:"action"` // committed | kept_open | discarded
	Structure       []api.EditResult `json:"structure,omitempty"`
	Changes         []api.EditResult `json:"changes,omitempty"`
	Diff            *api.EditDiff    `json:"diff,omitempty"`
	VersionNumber   *int             `json:"version_number,omitempty"`
	AwaitingPublish bool             `json:"awaiting_publish,omitempty"`
}

func newWorkEditCommand(e *env) *cobra.Command {
	var dryRun, yes, keepOpen bool
	var message, session string

	cmd := &cobra.Command{
		Use:   "edit <work> [changes.json]",
		Short: "Change part of a work by exact replacement, committed as one version",
		Long: "Apply a file of exact replacements to a work -- draft or published -- " +
			"as one version, without a local copy of it.\n\n" +
			"The file is a JSON list of changes, or an object with `changes` and, for " +
			"a book, `structure`:\n\n" +
			"  [{\"language\": \"ru\", \"field\": \"content\", \"old\": \"teh\", \"new\": \"the\"}]\n\n" +
			"A text is edited by `language` + `field` (content, title, description); a " +
			"book by `division_id` + `field` (body, title), and `structure` creates, " +
			"moves or deletes its divisions. Each `old` must occur exactly once. The " +
			"batch is all or none.\n\n" +
			"--dry-run shows the diff and throws the session away; --yes commits it " +
			"with --message. --keep-open applies the changes and leaves the session " +
			"open: continue it with --session, and commit it with --session and no " +
			"file. A commit is refused if someone changed the same text meanwhile; " +
			"re-run without --session to apply your changes to the current text.",
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			var file *editFile
			if len(args) == 2 {
				var err error
				if file, err = readEditFile(args[1]); err != nil {
					return err
				}
			} else if session == "" {
				return fail(ExitBadArguments, "give a changes file, or --session to commit an open one")
			}
			commit := !dryRun && !keepOpen
			if commit && message == "" {
				return fail(ExitBadArguments, "--message is required to commit: say what the change does")
			}
			if err := needsConfirm(!commit, yes, "edit would commit a new version"); err != nil {
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
			report, err := runEdit(cmd.Context(), client, workID, session, file, commit, dryRun, message)
			if err != nil {
				return err
			}
			return e.emit(report, func(w io.Writer) { printEdit(w, report) })
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show the diff; discard the session.")
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm the commit.")
	cmd.Flags().BoolVar(&keepOpen, "keep-open", false, "Apply the changes and leave the session open.")
	cmd.Flags().StringVarP(&message, "message", "m", "", "What the change does, for the version history.")
	cmd.Flags().StringVar(&session, "session", "", "Continue an open session instead of opening one.")
	return cmd
}

func runEdit(ctx context.Context, client *api.Client, workID, sessionID string, file *editFile,
	commit, dryRun bool, message string) (*editReport, error) {
	opened := false
	if sessionID == "" {
		s, err := client.OpenEdit(ctx, workID, "lt")
		if err != nil {
			return nil, err
		}
		sessionID, opened = s.ID, true
	}
	report := &editReport{WorkID: workID, SessionID: sessionID}
	// A session this run opened is not left behind by a refusal: it holds
	// nothing, and an empty open session only confuses `list_edits`.
	abandon := func(err error) (*editReport, error) {
		if opened {
			_ = client.DiscardEdit(ctx, workID, sessionID)
		}
		return nil, err
	}

	if file != nil {
		if len(file.Structure) > 0 {
			results, err := client.ApplyStructure(ctx, workID, sessionID, file.Structure)
			if err != nil {
				return abandon(err)
			}
			report.Structure = results
		}
		if len(file.Changes) > 0 {
			results, err := client.ApplyChanges(ctx, workID, sessionID, file.Changes)
			if err != nil {
				return abandon(err)
			}
			report.Changes = results
		}
	}
	diff, err := client.DiffEdit(ctx, workID, sessionID)
	if err != nil {
		return abandon(err)
	}
	report.Diff = diff

	switch {
	case dryRun:
		if err := client.DiscardEdit(ctx, workID, sessionID); err != nil {
			return nil, err
		}
		report.Action = "discarded"
	case !commit:
		report.Action = "kept_open"
	default:
		done, err := client.CommitEdit(ctx, workID, sessionID, message)
		if err != nil {
			return nil, err
		}
		report.Action = "committed"
		report.VersionNumber = done.VersionNumber
		report.AwaitingPublish = done.AwaitingPublish
	}
	return report, nil
}

func printEdit(w io.Writer, r *editReport) {
	if r.Diff != nil {
		for _, line := range r.Diff.Structure {
			fmt.Fprintf(w, "  %s\n", line)
		}
		for _, t := range r.Diff.Targets {
			fmt.Fprint(w, t.Diff)
		}
	}
	for _, s := range r.Structure {
		if s.DivisionID != "" {
			fmt.Fprintf(w, "  op %d -> %s\n", s.Index, s.DivisionID)
		}
	}
	switch r.Action {
	case "discarded":
		fmt.Fprintln(w, "dry run: nothing committed, session discarded")
	case "kept_open":
		fmt.Fprintf(w, "session %s is open; commit with --session %s -m \"...\" --yes\n", r.SessionID, r.SessionID)
	case "committed":
		if r.VersionNumber != nil {
			fmt.Fprintf(w, "committed as version %d\n", *r.VersionNumber)
		} else {
			fmt.Fprintln(w, "committed")
		}
		if r.AwaitingPublish {
			fmt.Fprintln(w, "a published record: the new version waits to be published as a corrigendum")
		}
	}
}
