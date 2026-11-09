package cli

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/The-LibreTimes/libretimes-cli/internal/api"
	"github.com/The-LibreTimes/libretimes-cli/internal/frontmatter"
)

// A <work> or <collection> argument is one of three things, tried in this
// order:
//
//   - a file on disk: its frontmatter `import_key` names the item, the same
//     lookup `publish` does, so the file you published from is the handle you
//     keep using and no id has to be copied anywhere;
//   - a bare UUID;
//   - a site or API URL with the UUID in its path (`/works/<id>`,
//     `/collections/<id>`).
//
// A path is tried first because a file literally named like a UUID is still a
// file the user pointed at.

// Codes for a target that names nothing usable. Stable, like every code here.
const (
	codeNotPublished = "not_published"
	codeDeleted      = "deleted"
)

// resolveWork turns a <work> argument into a publication id.
func resolveWork(ctx context.Context, client *api.Client, arg string) (string, error) {
	if isFile(arg) {
		raw, err := os.ReadFile(arg)
		if err != nil {
			return "", fail(ExitBadArguments, "%s: %v", arg, err)
		}
		meta, _, err := frontmatter.Split[frontmatter.Meta](string(raw))
		if err != nil {
			return "", fail(ExitBadArguments, "%s: %v", arg, err)
		}
		key := strings.TrimSpace(meta.ImportKey)
		if key == "" {
			key = defaultImportKey(arg)
		}
		ref, err := client.FindPublicationByImportKey(ctx, key)
		if err != nil {
			return "", err
		}
		if ref == nil {
			return "", &api.Error{Code: codeNotPublished, Message: fmt.Sprintf(
				"%s: none of your works carries the import_key %q; publish it first with `lt work publish`",
				arg, key)}
		}
		if ref.Removed {
			return "", &api.Error{Code: codeDeleted, Message: fmt.Sprintf(
				"%s: the work holding %q (%s) is deleted", arg, key, ref.ID)}
		}
		return strings.ToLower(ref.ID), nil
	}
	if id, ok := idFromArg(arg, "works", "publications"); ok {
		return id, nil
	}
	return "", fail(ExitBadArguments,
		"%s is not a file, a work id, or a /works/<id> URL", arg)
}

// resolveCollection turns a <collection> argument into a collection id.
func resolveCollection(ctx context.Context, client *api.Client, arg string) (string, error) {
	if isFile(arg) {
		raw, err := os.ReadFile(arg)
		if err != nil {
			return "", fail(ExitBadArguments, "%s: %v", arg, err)
		}
		meta, _, err := frontmatter.Split[frontmatter.CollectionMeta](string(raw))
		if err != nil {
			return "", fail(ExitBadArguments, "%s: %v", arg, err)
		}
		key := strings.TrimSpace(meta.ImportKey)
		if key == "" {
			return "", fail(ExitBadArguments, "%s has no `import_key`", arg)
		}
		ref, err := client.FindCollectionByImportKey(ctx, key)
		if err != nil {
			return "", err
		}
		if ref == nil {
			return "", &api.Error{Code: codeNotPublished, Message: fmt.Sprintf(
				"%s: none of your collections carries the import_key %q; publish it first with "+
					"`lt collection publish`", arg, key)}
		}
		return strings.ToLower(ref.ID), nil
	}
	if id, ok := idFromArg(arg, "collections"); ok {
		return id, nil
	}
	return "", fail(ExitBadArguments,
		"%s is not a file, a collection id, or a /collections/<id> URL", arg)
}

func isFile(arg string) bool {
	info, err := os.Stat(arg)
	return err == nil && !info.IsDir()
}

// idFromArg accepts a bare UUID, or a URL whose path has one right after one of
// the given segments. A UUID elsewhere in a URL is not taken: `/@ada/works`
// followed by some other id is not this kind of thing.
func idFromArg(arg string, segments ...string) (string, bool) {
	if uuidPattern.MatchString(arg) {
		return strings.ToLower(arg), true
	}
	u, err := url.Parse(arg)
	if err != nil || u.Path == "" {
		return "", false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i := 0; i+1 < len(parts); i++ {
		for _, seg := range segments {
			if parts[i] == seg && uuidPattern.MatchString(parts[i+1]) {
				return strings.ToLower(parts[i+1]), true
			}
		}
	}
	return "", false
}

// needsConfirm is the one write gate every command shares: a write without
// --yes or --dry-run stops before any request, naming what it would have done.
func needsConfirm(dryRun, yes bool, what string) error {
	if dryRun || yes {
		return nil
	}
	return fail(ExitNeedsConfirm,
		"%s. Re-run with --yes to confirm, or --dry-run to see the plan.", what)
}
