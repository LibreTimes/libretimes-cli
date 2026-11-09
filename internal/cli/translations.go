package cli

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/The-LibreTimes/libretimes-cli/internal/api"
	"github.com/The-LibreTimes/libretimes-cli/internal/config"
	"github.com/The-LibreTimes/libretimes-cli/internal/frontmatter"
)

// Translations, shared by `lt work translations` and `lt collection
// translations`. A translation file is its language's frontmatter (`language`,
// `title`, `description`) and, for a work, the translated text as its body. A
// collection has no body, so a collection translation file is frontmatter only.
//
// The original language is not a translation: it is the item's own text,
// written by `publish`. Setting it here would fork the one text into two that
// silently disagree, so it is refused.

// What a translation file resolves to.
const (
	translationCreate    = "create"
	translationUpdate    = "update"
	translationUnchanged = "unchanged"
	translationDelete    = "delete"
)

// translationResult is one file's (or one language's) outcome.
type translationResult struct {
	Path     string   `json:"path,omitempty"`
	Language string   `json:"language"`
	Action   string   `json:"action"`
	Changes  []string `json:"changes,omitempty"`
	Code     string   `json:"code,omitempty"`
	Message  string   `json:"message,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

func (r *translationResult) setFailure(err error) {
	r.Action = actionFailed
	r.Code, r.Message = codeAndMessage(err)
}

func (r *translationResult) invalid(format string, args ...any) {
	r.Action = actionFailed
	r.Code = "validation_error"
	r.Message = fmt.Sprintf(format, args...)
}

// translationSubject is what differs between a work and a collection.
type translationSubject struct {
	target  api.TranslationTarget
	noun    string // "work" or "collection", for messages
	hasBody bool
	resolve func(ctx context.Context, client *api.Client, arg string) (string, error)
	// primary reads the item's original language, the one language that is
	// not a translation.
	primary func(ctx context.Context, client *api.Client, id string) (string, error)
}

var workTranslations = translationSubject{
	target:  api.TranslateWork,
	noun:    "work",
	hasBody: true,
	resolve: resolveWork,
	primary: func(ctx context.Context, client *api.Client, id string) (string, error) {
		w, err := client.GetWork(ctx, id)
		if err != nil {
			return "", err
		}
		return w.Language, nil
	},
}

var collectionTranslations = translationSubject{
	target:  api.TranslateCollection,
	noun:    "collection",
	hasBody: false,
	resolve: resolveCollection,
	primary: func(ctx context.Context, client *api.Client, id string) (string, error) {
		c, err := client.GetCollection(ctx, id)
		if err != nil {
			return "", err
		}
		return c.PrimaryLanguage, nil
	},
}

func newTranslationsCommand(e *env, s translationSubject) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "translations",
		Short: fmt.Sprintf("List, set and remove a %s's translations", s.noun),
	}
	cmd.AddCommand(newTranslationsSetCommand(e, s), newTranslationsLsCommand(e, s),
		newTranslationsRmCommand(e, s))
	return cmd
}

func newTranslationsSetCommand(e *env, s translationSubject) *cobra.Command {
	var dryRun, yes bool
	body := "and, below it, the translated text"
	if !s.hasBody {
		body = "and nothing below it (a collection has no body)"
	}
	cmd := &cobra.Command{
		Use:   fmt.Sprintf("set <%s> <file.md>...", s.noun),
		Short: fmt.Sprintf("Create or update a %s's translations from files", s.noun),
		Long: fmt.Sprintf("Create or update one language of a %s per file.\n\n"+
			"Each file carries `language`, `title` and `description` in its frontmatter %s. "+
			"A language already on the site is updated only in the fields that differ; an "+
			"unchanged file writes nothing. The %s's original language is refused: that "+
			"text is the %s itself.", s.noun, body, s.noun, s.noun),
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, err := resolveFiles(args[1:])
			if err != nil {
				return err
			}
			if err := needsConfirm(dryRun, yes,
				fmt.Sprintf("translations set would write %d language(s)", len(paths))); err != nil {
				return err
			}
			client, _, err := e.client(cmd.Context(), true, config.ProductLibreTimes)
			if err != nil {
				return err
			}
			id, err := s.resolve(cmd.Context(), client, args[0])
			if err != nil {
				return err
			}
			primary, err := s.primary(cmd.Context(), client, id)
			if err != nil {
				return err
			}

			seen := map[string]string{}
			results := make([]translationResult, 0, len(paths))
			for _, path := range paths {
				r := setTranslation(cmd.Context(), client, s, id, primary, path, dryRun, seen)
				results = append(results, r)
			}
			return reportTranslations(e, results)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show the plan; write nothing.")
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm the writes.")
	return cmd
}

// setTranslation reconciles one file. seen catches two files for one language
// in the same run, which would otherwise have the second silently win.
func setTranslation(ctx context.Context, client *api.Client, s translationSubject, id, primary, path string, dryRun bool, seen map[string]string) translationResult {
	r := translationResult{Path: path}
	raw, err := os.ReadFile(path)
	if err != nil {
		r.setFailure(err)
		return r
	}
	meta, text, err := frontmatter.Split[frontmatter.TranslationMeta](string(raw))
	if err != nil {
		r.setFailure(err)
		return r
	}
	r.Language = strings.TrimSpace(meta.Language)
	text = normalizeText(text)
	title, description := strings.TrimSpace(meta.Title), strings.TrimSpace(meta.Description)
	switch {
	case r.Language == "":
		r.invalid("frontmatter has no `language`, the language this file is written in")
		return r
	case strings.EqualFold(r.Language, primary):
		r.Code, r.Action = "primary_language", actionFailed
		r.Message = fmt.Sprintf("%s is the %s's original language, not a translation; change "+
			"that text with `lt %s publish`", r.Language, s.noun, s.noun)
		return r
	case seen[r.Language] != "":
		r.invalid("%s is also set by %s; one file per language", r.Language, seen[r.Language])
		return r
	case title == "":
		r.invalid("frontmatter has no `title`")
		return r
	case !s.hasBody && text != "":
		r.invalid("a collection translation is frontmatter only; this file has a body")
		return r
	case s.hasBody && text == "":
		r.invalid("the file has no body: a work's translation is its translated text")
		return r
	}
	seen[r.Language] = path

	in := api.TranslationInput{Title: &title}
	if description != "" {
		in.Description = &description
	}
	if s.hasBody {
		in.Content = &text
	}

	current, err := client.GetTranslation(ctx, s.target, id, r.Language)
	if err != nil {
		r.setFailure(err)
		return r
	}
	if current == nil || (current.Title == nil && current.Description == nil && current.Content == nil) {
		// An empty row is a language nobody has written yet.
		r.Action = translationCreate
	} else {
		r.Changes = translationChanges(in, current)
		if len(r.Changes) == 0 {
			r.Action = translationUnchanged
			return r
		}
		r.Action = translationUpdate
	}
	if dryRun {
		return r
	}

	stored, err := client.PutTranslation(ctx, s.target, id, r.Language, in)
	if err != nil {
		r.setFailure(err)
		return r
	}
	if s.hasBody && normalizeText(deref(stored.Content)) != text {
		// The platform normalises some markdown on the way in. The text is
		// saved, but this file will report `update` on every run until it
		// matches what was stored.
		r.Warnings = append(r.Warnings, "the platform stored the text with changes; this file will "+
			"report update on every run until it matches")
	}
	return r
}

// translationChanges names the fields the file would change. A field the file
// does not state is not sent, so it cannot be a change.
func translationChanges(in api.TranslationInput, cur *api.Translation) []string {
	var changes []string
	if in.Title != nil && *in.Title != strings.TrimSpace(deref(cur.Title)) {
		changes = append(changes, "title")
	}
	if in.Description != nil && *in.Description != strings.TrimSpace(deref(cur.Description)) {
		changes = append(changes, "description")
	}
	if in.Content != nil && *in.Content != normalizeText(deref(cur.Content)) {
		changes = append(changes, "content")
	}
	return changes
}

// normalizeText makes a file and a stored body comparable: line endings and
// the whitespace at either end are not the text.
func normalizeText(s string) string {
	return strings.TrimSpace(strings.ReplaceAll(s, "\r\n", "\n"))
}

func newTranslationsLsCommand(e *env, s translationSubject) *cobra.Command {
	return &cobra.Command{
		Use:   fmt.Sprintf("ls <%s>", s.noun),
		Short: fmt.Sprintf("List a %s's languages", s.noun),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := e.client(cmd.Context(), true, config.ProductLibreTimes)
			if err != nil {
				return err
			}
			id, err := s.resolve(cmd.Context(), client, args[0])
			if err != nil {
				return err
			}
			overview, err := client.GetTranslations(cmd.Context(), s.target, id)
			if err != nil {
				return err
			}
			return e.emit(overview, func(w io.Writer) {
				fmt.Fprintf(w, "  %-6s %-24s %s\n", overview.PrimaryLanguage, "(original)", "")
				for _, l := range overview.Languages {
					if l.Language == overview.PrimaryLanguage {
						continue
					}
					state := "not served"
					if l.IsPublished {
						state = "served"
					}
					fmt.Fprintf(w, "  %-6s %-24s %d/%d fields, %s\n", l.Language, l.LanguageName,
						l.TranslatedFields, l.TotalFields, state)
				}
			})
		},
	}
}

func newTranslationsRmCommand(e *env, s translationSubject) *cobra.Command {
	var dryRun, yes bool
	cmd := &cobra.Command{
		Use:   fmt.Sprintf("rm <%s> <language>...", s.noun),
		Short: fmt.Sprintf("Remove languages from a %s", s.noun),
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := needsConfirm(dryRun, yes,
				fmt.Sprintf("translations rm would delete %d language(s)", len(args)-1)); err != nil {
				return err
			}
			client, _, err := e.client(cmd.Context(), true, config.ProductLibreTimes)
			if err != nil {
				return err
			}
			id, err := s.resolve(cmd.Context(), client, args[0])
			if err != nil {
				return err
			}
			primary, err := s.primary(cmd.Context(), client, id)
			if err != nil {
				return err
			}
			results := make([]translationResult, 0, len(args)-1)
			for _, lang := range args[1:] {
				r := translationResult{Language: lang}
				switch current, err := client.GetTranslation(cmd.Context(), s.target, id, lang); {
				case strings.EqualFold(lang, primary):
					r.Code, r.Action = "primary_language", actionFailed
					r.Message = fmt.Sprintf("%s is the %s's original language and cannot be removed", lang, s.noun)
				case err != nil:
					r.setFailure(err)
				case current == nil:
					r.Code, r.Action = api.CodeNotFound, actionFailed
					r.Message = fmt.Sprintf("the %s has no %s translation", s.noun, lang)
				default:
					r.Action = translationDelete
					if !dryRun {
						if err := client.DeleteTranslation(cmd.Context(), s.target, id, lang); err != nil {
							r.setFailure(err)
						}
					}
				}
				results = append(results, r)
			}
			return reportTranslations(e, results)
		},
	}
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show the plan; write nothing.")
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm the writes.")
	return cmd
}

func reportTranslations(e *env, results []translationResult) error {
	failed := false
	for _, r := range results {
		if r.Action == actionFailed {
			failed = true
		}
		if e.jsonOut {
			continue
		}
		if r.Action == actionFailed {
			fmt.Fprintf(e.err, "  %-9s %-6s %s: %s\n", r.Action, r.Language, r.Code, r.Message)
			continue
		}
		if !e.quiet {
			trailing := r.Path
			if len(r.Changes) > 0 {
				trailing = strings.TrimSpace(trailing + " [" + strings.Join(r.Changes, ", ") + "]")
			}
			fmt.Fprintf(e.out, "  %-9s %-6s %s\n", r.Action, r.Language, trailing)
		}
		for _, w := range r.Warnings {
			fmt.Fprintf(e.err, "    warning: %s\n", w)
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
}
