package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/The-LibreTimes/libretimes-cli/internal/api"
	"github.com/The-LibreTimes/libretimes-cli/internal/config"
)

// Attachments are the files bound to a work -- a notebook beside a problem
// sheet, a dataset beside a paper. A work is published first (a draft is
// fine), then its files are added to it; they are never part of the markdown.
//
// A file is identified by its name. Adding a name the work already has is
// `unchanged` when the bytes are the same and `blocked` when they differ,
// unless --replace says to swap it: overwriting a file readers may already
// have downloaded is a decision, not a side effect of a re-run. The comparison
// is by content (sha256 of the stored file), not by size or date.
//
// A work whose text is a file (a PDF or DOCX face) keeps that file among its
// attachments. It is shown here and never touched: it is the work's body, and
// changing it is `lt work publish`'s business.

const (
	attachCreate    = "create"
	attachReplace   = "replace"
	attachUnchanged = "unchanged"
	attachRemove    = "remove"
)

// attachmentResult is one file's outcome.
type attachmentResult struct {
	Path         string `json:"path,omitempty"`
	Name         string `json:"name"`
	Action       string `json:"action"`
	AttachmentID string `json:"attachment_id,omitempty"`
	// Replaces is the id of the file a --replace swapped out.
	Replaces  string `json:"replaces,omitempty"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
	Code      string `json:"code,omitempty"`
	Message   string `json:"message,omitempty"`
}

func (r *attachmentResult) setFailure(err error) {
	r.Action = actionFailed
	r.Code, r.Message = codeAndMessage(err)
}

// attachmentsResult is one command's outcome: the work, and each file.
type attachmentsResult struct {
	PublicationID string             `json:"publication_id"`
	Files         []attachmentResult `json:"files"`
}

func newWorkAttachmentsCommand(e *env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "attachments",
		Short: "List, add and remove the files attached to a work",
	}
	cmd.AddCommand(newAttachmentsAddCommand(e), newAttachmentsLsCommand(e), newAttachmentsRmCommand(e))
	return cmd
}

func newAttachmentsAddCommand(e *env) *cobra.Command {
	var dryRun, yes, replace bool
	cmd := &cobra.Command{
		Use:   "add <work> <file>...",
		Short: "Attach files to a work",
		Long: "Upload files and attach them to a work, keeping every file already attached.\n\n" +
			"<work> is the markdown file the work was published from, its id, or its " +
			"/works/<id> URL. A file whose name the work already has is `unchanged` when " +
			"the bytes match, and `blocked` when they differ unless --replace is given. " +
			"Run with --dry-run first.",
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, err := resolveFiles(args[1:])
			if err != nil {
				return err
			}
			if err := uniqueNames(paths); err != nil {
				return err
			}
			if err := needsConfirm(dryRun, yes,
				fmt.Sprintf("attachments add would upload up to %d file(s)", len(paths))); err != nil {
				return err
			}
			client, _, err := e.client(cmd.Context(), true, config.ProductLibreTimes)
			if err != nil {
				return err
			}
			id, err := resolveWork(cmd.Context(), client, args[0])
			if err != nil {
				return err
			}
			result, err := addAttachments(cmd.Context(), client, id, paths, replace, dryRun)
			if err != nil {
				return err
			}
			return reportAttachments(e, result)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show the plan; write nothing.")
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm the writes.")
	cmd.Flags().BoolVar(&replace, "replace", false,
		"Swap an attached file whose name matches but whose content differs.")
	return cmd
}

// uniqueNames refuses two arguments with one file name: the second would be
// compared against the first's upload, which is never what was meant.
func uniqueNames(paths []string) error {
	seen := map[string]string{}
	for _, p := range paths {
		name := filepath.Base(p)
		if first, dup := seen[name]; dup {
			return fail(ExitBadArguments, "%s and %s have the same name; a work's files are told apart by name",
				first, p)
		}
		seen[name] = p
	}
	return nil
}

// planned is one file's decision before anything is written.
type planned struct {
	result *attachmentResult
	data   []byte
	oldID  string // the id a replace swaps out
}

// addAttachments plans every file, then uploads, then binds once.
//
// Every file is read and compared before the first upload, so a dry run
// reports exactly what the real run does, and a blocked file stops nothing but
// itself. The bind is one write of the whole list, built from the list just
// read: files not named here keep their places.
func addAttachments(ctx context.Context, client *api.Client, id string, paths []string, replace, dryRun bool) (attachmentsResult, error) {
	out := attachmentsResult{PublicationID: id}
	work, err := client.GetWork(ctx, id)
	if err != nil {
		return out, err
	}

	plans := make([]*planned, 0, len(paths))
	for _, path := range paths {
		r := &attachmentResult{Path: path, Name: filepath.Base(path)}
		p := &planned{result: r}
		plans = append(plans, p)

		data, err := os.ReadFile(path)
		if err != nil {
			r.setFailure(err)
			continue
		}
		p.data = data
		r.SizeBytes = int64(len(data))

		existing := findByName(work.Attachments, r.Name)
		switch {
		case existing == nil:
			r.Action = attachCreate
		case strings.EqualFold(existing.ID, work.BodyAttachmentID):
			r.Action, r.Code = actionBlocked, "file_face"
			r.AttachmentID = existing.ID
			r.Message = "this name is the work's own text (its file view); change that with `lt work publish`"
		default:
			same, err := sameContent(ctx, client, *existing, data)
			if err != nil {
				r.setFailure(fmt.Errorf("comparing with the attached %s: %w", r.Name, err))
				continue
			}
			r.AttachmentID = existing.ID
			switch {
			case same:
				r.Action = attachUnchanged
			case replace:
				r.Action, p.oldID, r.Replaces = attachReplace, existing.ID, existing.ID
			default:
				r.Action, r.Code = actionBlocked, "attachment_differs"
				r.Message = fmt.Sprintf("a different file named %s is attached (%s); pass --replace to swap it",
					r.Name, existing.ID)
			}
		}
	}

	for _, p := range plans {
		out.Files = append(out.Files, *p.result)
	}
	if dryRun {
		return out, nil
	}

	ids := slices.Clone(work.AttachmentIDs)
	changed := false
	for i, p := range plans {
		r := p.result
		if r.Action != attachCreate && r.Action != attachReplace {
			continue
		}
		uploaded, err := client.UploadAttachment(ctx, r.Name, contentTypeOf(r.Name), p.data)
		if err != nil {
			r.setFailure(err)
			out.Files[i] = *r
			continue
		}
		r.AttachmentID = uploaded.ID
		if p.oldID != "" {
			ids = swapID(ids, p.oldID, uploaded.ID)
		} else {
			ids = append(ids, uploaded.ID)
		}
		changed = true
		out.Files[i] = *r
	}

	// Bind whatever uploaded, even when another file failed: an upload left
	// unbound is a file nobody can reach, and the failed one is reported.
	if changed {
		if err := client.SetAttachmentIDs(ctx, id, ids); err != nil {
			for i := range out.Files {
				if a := out.Files[i].Action; a == attachCreate || a == attachReplace {
					out.Files[i].Action = actionFailed
					out.Files[i].Code, out.Files[i].Message = codeAndMessage(
						fmt.Errorf("uploaded, but attaching it to the work failed: %w", err))
				}
			}
		}
	}
	return out, nil
}

func findByName(atts []api.Attachment, name string) *api.Attachment {
	for i := range atts {
		if atts[i].OriginalName == name {
			return &atts[i]
		}
	}
	return nil
}

// sameContent compares a local file with an attached one. A size mismatch
// settles it without a download.
func sameContent(ctx context.Context, client *api.Client, a api.Attachment, data []byte) (bool, error) {
	if a.SizeBytes != int64(len(data)) {
		return false, nil
	}
	stored, err := client.DownloadAttachment(ctx, a)
	if err != nil {
		return false, err
	}
	want, got := sha256.Sum256(data), sha256.Sum256(stored)
	return bytes.Equal(want[:], got[:]), nil
}

func swapID(ids []string, old, replacement string) []string {
	for i, id := range ids {
		if strings.EqualFold(id, old) {
			ids[i] = replacement
			return ids
		}
	}
	return append(ids, replacement)
}

// contentTypeOf declares a type for the upload. Storage sniffs the bytes and
// trusts its own reading over this; the declaration only matters for types
// with no magic number, which is most text formats.
func contentTypeOf(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".ipynb":
		return "application/x-ipynb+json"
	case ".py":
		return "text/x-python"
	case ".md":
		return "text/markdown"
	case ".csv":
		return "text/csv"
	case ".json":
		return "application/json"
	case ".tex":
		return "application/x-tex"
	}
	if t := mime.TypeByExtension(filepath.Ext(name)); t != "" {
		return t
	}
	return "application/octet-stream"
}

func newAttachmentsLsCommand(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "ls <work>",
		Short: "List the files attached to a work",
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
			return e.emit(work.Attachments, func(w io.Writer) {
				if len(work.Attachments) == 0 {
					fmt.Fprintln(w, "  no files attached")
				}
				for _, a := range work.Attachments {
					note := ""
					if strings.EqualFold(a.ID, work.BodyAttachmentID) {
						note = "  (the work's file view)"
					}
					fmt.Fprintf(w, "  %-36s %10s  %-28s %s%s\n", a.ID, humanSize(a.SizeBytes),
						a.ContentType, a.OriginalName, note)
				}
			})
		},
	}
}

func newAttachmentsRmCommand(e *env) *cobra.Command {
	var dryRun, yes bool
	cmd := &cobra.Command{
		Use:   "rm <work> <name|id>...",
		Short: "Detach files from a work",
		Long: "Detach files from a work, by file name or attachment id. The work's own file " +
			"view is refused. Run with --dry-run first.",
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := needsConfirm(dryRun, yes,
				fmt.Sprintf("attachments rm would detach %d file(s)", len(args)-1)); err != nil {
				return err
			}
			client, _, err := e.client(cmd.Context(), true, config.ProductLibreTimes)
			if err != nil {
				return err
			}
			id, err := resolveWork(cmd.Context(), client, args[0])
			if err != nil {
				return err
			}
			result, err := removeAttachments(cmd.Context(), client, id, args[1:], dryRun)
			if err != nil {
				return err
			}
			return reportAttachments(e, result)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show the plan; write nothing.")
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm the writes.")
	return cmd
}

func removeAttachments(ctx context.Context, client *api.Client, id string, names []string, dryRun bool) (attachmentsResult, error) {
	out := attachmentsResult{PublicationID: id}
	work, err := client.GetWork(ctx, id)
	if err != nil {
		return out, err
	}
	drop := map[string]bool{}
	for _, name := range names {
		r := attachmentResult{Name: name}
		var hit *api.Attachment
		for i := range work.Attachments {
			a := &work.Attachments[i]
			if strings.EqualFold(a.ID, name) || a.OriginalName == name {
				hit = a
				break
			}
		}
		switch {
		case hit == nil:
			r.Action, r.Code = actionFailed, api.CodeNotFound
			r.Message = "no file by that name or id is attached to this work"
		case strings.EqualFold(hit.ID, work.BodyAttachmentID):
			r.Action, r.Code, r.AttachmentID = actionBlocked, "file_face", hit.ID
			r.Message = "this is the work's own text (its file view); change that with `lt work publish`"
		default:
			r.Action, r.AttachmentID, r.Name = attachRemove, hit.ID, hit.OriginalName
			drop[strings.ToLower(hit.ID)] = true
		}
		out.Files = append(out.Files, r)
	}
	if dryRun || len(drop) == 0 {
		return out, nil
	}
	keep := make([]string, 0, len(work.AttachmentIDs))
	for _, a := range work.AttachmentIDs {
		if !drop[strings.ToLower(a)] {
			keep = append(keep, a)
		}
	}
	if err := client.SetAttachmentIDs(ctx, id, keep); err != nil {
		for i := range out.Files {
			if out.Files[i].Action == attachRemove {
				out.Files[i].Action = actionFailed
				out.Files[i].Code, out.Files[i].Message = codeAndMessage(err)
			}
		}
	}
	return out, nil
}

func reportAttachments(e *env, result attachmentsResult) error {
	failed := false
	for _, f := range result.Files {
		if f.Action == actionFailed || f.Action == actionBlocked {
			failed = true
		}
		if e.jsonOut {
			continue
		}
		if f.Action == actionFailed || f.Action == actionBlocked {
			fmt.Fprintf(e.err, "  %-9s %-32s %s: %s\n", f.Action, f.Name, f.Code, f.Message)
			continue
		}
		if !e.quiet {
			fmt.Fprintf(e.out, "  %-9s %-32s %s\n", f.Action, f.Name, f.AttachmentID)
		}
	}
	if e.jsonOut {
		if err := e.emit(result, func(io.Writer) {}); err != nil {
			return err
		}
	}
	if failed {
		return &exitError{code: ExitAPIError, err: errQuiet{}}
	}
	return nil
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
