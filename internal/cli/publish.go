package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/The-LibreTimes/libretimes-cli/internal/api"
	"github.com/The-LibreTimes/libretimes-cli/internal/config"
	"github.com/The-LibreTimes/libretimes-cli/internal/frontmatter"
)

// Actions a file can resolve to. Named rather than boolean because "blocked"
// is a third outcome, not a failed update.
const (
	actionCreate  = "create"
	actionUpdate  = "update"
	actionBlocked = "blocked"
	actionFailed  = "failed"
)

// fileResult is one file's outcome, and the shape --json emits.
//
// Code is separate from Message because AGENTS.md promises a *stable* code on
// per-file results and an agent should branch on it, not parse prose. It was
// previously only ever glued onto the front of Message, which meant the
// documented contract could only be met with a string split.
type fileResult struct {
	Path          string `json:"path"`
	ImportKey     string `json:"import_key"`
	Action        string `json:"action"`
	PublicationID string `json:"publication_id,omitempty"`
	Code          string `json:"code,omitempty"`
	Message       string `json:"message,omitempty"`
	// Contributors is the byline plan for the file's `contributors`, absent
	// when the file lists none.
	Contributors []contributorResult `json:"contributors,omitempty"`
}

// setFailure records an error as both a stable code and a human message.
func (r *fileResult) setFailure(err error) {
	r.Action = actionFailed
	r.Code, r.Message = codeAndMessage(err)
}

// codeAndMessage splits an error into its stable code and its prose. A
// non-API failure (an unreadable file, malformed YAML) has no API code, so it
// gets a local slug rather than an empty string an agent cannot branch on.
//
// The message is the whole wrapped chain, not just the API's part, so a
// failure can say which contributor it was about. What a wrapper adds is only
// ever the caller's own input, never upstream detail, so the 404 constant
// stays byte-identical inside it.
func codeAndMessage(err error) (string, string) {
	var apiErr *api.Error
	if errors.As(err, &apiErr) {
		return apiErr.Code, err.Error()
	}
	return "local_error", err.Error()
}

// resolveFiles turns command-line arguments into a list of files to
// reconcile.
//
// A literal path is checked before anything else, glob metacharacters and
// all: a real file named `[draft].md` must still resolve, and a shell that
// already expanded a wildcard (bash, zsh) hands lt literal paths it should
// never re-glob. Only an argument that does not exist as a literal path and
// contains a metacharacter is expanded here.
//
// This is what makes `lt publish content/*.md` work on Windows: neither
// PowerShell nor cmd.exe expands `*` before handing it to the process, unlike
// every POSIX shell (WINDOWS.md #1). Running the expansion unconditionally
// rather than gating it on GOOS is what `gh` and `rg` do, and it costs
// nothing under a shell that already expanded — the arguments it hands lt
// contain no metacharacters left to match.
func resolveFiles(args []string) ([]string, error) {
	paths := make([]string, 0, len(args))
	for _, raw := range args {
		info, err := os.Stat(raw)
		if err == nil {
			if info.IsDir() {
				return nil, fail(ExitBadArguments,
					"%s is a directory. Pass files, or a glob (e.g. %s/*.md) to select the ones inside it.",
					raw, filepath.ToSlash(filepath.Clean(raw)))
			}
			paths = append(paths, raw)
			continue
		}

		if !hasGlobMeta(raw) {
			return nil, fail(ExitBadArguments, "no such file: %s", raw)
		}
		if strings.Contains(raw, "**") {
			// filepath.Glob treats ** as a single *, matching one directory
			// level rather than every level beneath it — silently running a
			// narrower publish than the pattern asked for is worse than
			// refusing, so this is a hard error rather than a quiet partial
			// match.
			return nil, fail(ExitBadArguments,
				"%s: lt does not expand \"**\" (it matches one level, like a single \"*\", "+
					"which is not what \"**\" is supposed to mean). Run lt once per "+
					"directory, or rely on your shell's own recursive glob (bash: "+
					"`shopt -s globstar`) so lt receives already-expanded, literal paths.",
				raw)
		}

		matches, globErr := filepath.Glob(raw)
		if globErr != nil {
			// The only error filepath.Glob returns is a malformed pattern
			// (ErrBadPattern, an unmatched `[`) -- never "no matches", which
			// is an empty slice, not an error.
			return nil, fail(ExitBadArguments, "%s: %v", raw, globErr)
		}
		if len(matches) == 0 {
			return nil, fail(ExitBadArguments, "%s matched no files", raw)
		}
		for _, m := range matches {
			info, err := os.Stat(m)
			if err != nil {
				return nil, fail(ExitBadArguments, "%s: %v", m, err)
			}
			if info.IsDir() {
				// A directory among a glob's matches (`content/*` catching a
				// subdirectory alongside its files) is skipped rather than
				// refused -- unlike an explicit directory argument, nobody
				// typed this path on purpose.
				continue
			}
			paths = append(paths, m)
		}
	}
	return paths, nil
}

// hasGlobMeta reports whether path contains a glob metacharacter. Mirrors the
// unexported check path/filepath uses internally to decide the same thing.
func hasGlobMeta(path string) bool {
	return strings.ContainsAny(path, "*?[")
}

func newPublishCommand(e *env) *cobra.Command {
	var (
		dryRun bool
		yes    bool
	)

	cmd := &cobra.Command{
		Use:   "publish <file.md>...",
		Short: "Reconcile markdown files as publications",
		Long: "Reconcile one or more markdown files as LibreTimes publications.\n\n" +
			"The frontmatter is the import manifest: `import_key` is what makes a " +
			"re-run an update instead of a duplicate. Run with --dry-run first — it " +
			"reports create/update/blocked per file and writes nothing, so the first " +
			"real run is never a guess.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, err := resolveFiles(args)
			if err != nil {
				return err
			}

			if !dryRun && !yes {
				return fail(ExitNeedsConfirm,
					"publish would write %d publication(s). Re-run with --yes to "+
						"confirm, or --dry-run to see the plan.", len(paths))
			}

			// A token is required even for --dry-run. The plan's whole value
			// is saying which files would be created and which updated, and
			// that answer comes from reading your own publications. Without a
			// token the lookup 401s and every file reports an auth failure,
			// which reads as a broken publish rather than a missing
			// credential — so fail once, up front, with the reason.
			client, _, err := e.client(cmd.Context(), true, config.ProductLibreTimes)
			if err != nil {
				return err
			}

			results := make([]fileResult, 0, len(paths))
			failed := false
			profiles := newProfileResolver(client)
			for _, path := range paths {
				result := publishOne(cmd.Context(), client, path, dryRun, profiles)
				results = append(results, result)
				if result.Action == actionFailed || result.Action == actionBlocked {
					failed = true
				}
				if !e.jsonOut {
					renderResult(e, result)
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
	return cmd
}

// errQuiet marks a failure already reported per-file, so Execute does not
// print a second, vaguer line underneath the specific ones.
type errQuiet struct{}

func (errQuiet) Error() string { return "" }

func renderResult(e *env, r fileResult) {
	switch r.Action {
	case actionFailed, actionBlocked:
		detail := r.Message
		if r.Code != "" && r.Action == actionFailed {
			detail = r.Code + ": " + r.Message
		}
		fmt.Fprintf(e.err, "  %-8s %-40s %s\n", r.Action, r.ImportKey, detail)
	default:
		if e.quiet {
			return
		}
		trailing := r.PublicationID
		if trailing == "" {
			trailing = r.Path
		}
		fmt.Fprintf(e.out, "  %-8s %-40s %s\n", r.Action, r.ImportKey, trailing)
		for _, c := range r.Contributors {
			note := ""
			if c.CurrentRole != "" {
				note = fmt.Sprintf(" (already credited as %s; left unchanged)", c.CurrentRole)
			}
			fmt.Fprintf(e.out, "    %-8s %-10s %s%s\n", c.Action, c.Role, c.Profile, note)
		}
	}
}

// publishOne resolves and applies a single file.
//
// Find-then-update, in that order, always. Never create-then-fallback-on-
// conflict: a publication is soft-deleted and a removed row keeps both its
// import_key and its slot in the partial unique index, so for a removed row
// the create path 409s permanently and a fallback after the conflict is never
// reachable. Exactly the rows a fallback would need to handle are the rows it
// cannot reach.
//
// profiles caches handle lookups across the files of one run; nil gets a fresh
// cache.
func publishOne(ctx context.Context, client *api.Client, path string, dryRun bool, profiles *profileResolver) fileResult {
	result := fileResult{Path: path}
	if profiles == nil {
		profiles = newProfileResolver(client)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		result.setFailure(err)
		return result
	}

	meta, body, err := frontmatter.Split[frontmatter.Meta](string(raw))
	if err != nil {
		result.setFailure(err)
		return result
	}

	key := strings.TrimSpace(meta.ImportKey)
	if key == "" {
		key = defaultImportKey(path)
	}

	result.ImportKey = key

	if strings.TrimSpace(meta.Type) == "" {
		// Both publications-service and the BFF declare `type` required, so
		// this would fail at the API anyway — but failing here names the file
		// and the fix instead of relaying a 422.
		result.Action = actionFailed
		result.Code = "validation_error"
		result.Message = "frontmatter has no `type`, which is required and has no default"
		return result
	}

	input := buildInput(meta, body)

	credits, err := resolveContributors(ctx, profiles, meta.Contributors)
	if err != nil {
		result.setFailure(err)
		return result
	}

	existing, err := client.FindPublicationByImportKey(ctx, key)
	if err != nil {
		result.setFailure(err)
		return result
	}

	switch {
	case existing != nil && existing.Removed:
		// Named separately from "update" so a dry run does not promise work
		// the real run will refuse. Deletion is a soft delete: the row keeps
		// its import_key and its index slot, so this key cannot be recreated.
		// Restoring work its author withdrew is not something an import gets
		// to decide.
		result.Action = actionBlocked
		result.Code = "blocked"
		result.PublicationID = existing.ID
		result.Message = fmt.Sprintf(
			"a deleted publication (%s) holds this key; restore it or use another key",
			existing.ID)
		return result

	case existing != nil:
		result.Action = actionUpdate
		result.PublicationID = existing.ID
		if len(credits) > 0 {
			// The update body carries no byline, so the byline is reconciled
			// against what is there. Read before any write, so --dry-run
			// reports the same diff the real run acts on.
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
		if dryRun {
			return result
		}
		updated, err := client.UpdatePublication(ctx, existing.ID, input)
		if err != nil {
			result.setFailure(err)
			return result
		}
		result.PublicationID = updated.ID
		if err := inviteMissing(ctx, client, existing.ID, credits); err != nil {
			result.setFailure(err)
		}
		return result

	default:
		result.Action = actionCreate
		applyToCreate(&input, credits)
		result.Contributors = credits
		if dryRun {
			return result
		}
		input.ImportKey = key
		created, err := client.CreatePublication(ctx, input)
		if err != nil {
			result.setFailure(err)
			return result
		}
		result.PublicationID = created.ID
		return result
	}
}

func buildInput(meta frontmatter.Meta, body string) api.PublicationInput {
	return api.PublicationInput{
		Title:       meta.Title,
		Content:     body,
		Description: meta.Description,
		Type:        meta.Type,
		License:     meta.License,
		Language:    meta.Language,
		Place:       meta.Place,
		Visibility:  meta.Visibility,
		AIUsage:     meta.AIUsage,
		IsDraft:     meta.IsDraft,
		Tags:        meta.Tags,
		CategoryIDs: meta.CategoryIDs,
	}
}

// defaultImportKey derives a key from the file's path rather than its stem.
//
// The Python importer defaulted to `lt:<filename-stem>`, which collides the
// moment two directories share a slug — a ru/ and an en/ tree of the same
// articles being the obvious case, and a collision here silently overwrites
// one language with the other. Including the parent directory makes that
// case distinct. Set `import_key` explicitly and none of this applies.
func defaultImportKey(path string) string {
	cleaned := filepath.ToSlash(filepath.Clean(path))
	stem := strings.TrimSuffix(filepath.Base(cleaned), filepath.Ext(cleaned))
	parent := filepath.Base(filepath.Dir(cleaned))

	if parent == "." || parent == "/" || parent == "" {
		return "lt:" + stem
	}
	return "lt:" + parent + "/" + stem
}
