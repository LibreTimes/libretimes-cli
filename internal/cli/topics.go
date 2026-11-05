package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/The-LibreTimes/libretimes-cli/internal/api"
	"github.com/The-LibreTimes/libretimes-cli/internal/frontmatter"
)

// Help topics: what is not one command's. Each is a cobra command with no Run
// and no subcommands, which cobra lists under "Additional help topics" and
// prints for `lt help <topic>`. The generated manual renders the same Long as
// a page, so the terminal and the web read one text.
//
// The text is markdown, which reads plainly in a terminal and renders on the
// web. Tables are built from the code where the code holds the facts: the
// exit codes from their constants, the frontmatter from its structs, and the
// value lists from the api package. topics_test.go fails when the source
// gains an environment variable or an error code these pages do not name.

func newTopicCommands() []*cobra.Command {
	return []*cobra.Command{
		{
			Use:   "exit-codes",
			Short: "Exit codes, error codes, and the shape of a failure",
			Long:  exitCodesTopic(),
		},
		{
			Use:   "environment",
			Short: "Environment variables, and where lt keeps its credentials",
			Long:  environmentTopic,
		},
		{
			Use:   "frontmatter",
			Short: "The keys a work file, a course manifest and a book manifest may carry",
			Long:  frontmatterTopic(),
		},
	}
}

// isTopic reports whether cmd is a help topic rather than a command.
func isTopic(cmd *cobra.Command) bool {
	return !cmd.Runnable() && !cmd.HasSubCommands()
}

type exitCodeRow struct {
	code    int
	meaning string
	action  string
}

var exitCodeRows = []exitCodeRow{
	{ExitOK, "success", "continue"},
	{ExitAPIError, "the API refused the request", "read the error; its code says whether retrying helps"},
	{ExitBadArguments, "bad arguments", "fix the invocation; do not retry unchanged"},
	{ExitNeedsConfirm, "a write was attempted without `--yes`", "re-run with `--yes` once you intend the write"},
	{ExitNotSignedIn, "not signed in", "run `lt auth login` (or `--device`); do not retry the command first"},
}

type errorCodeRow struct {
	code  string
	retry string
}

// errorCodeRows are the codes a failure or a per-file result carries.
var errorCodeRows = []errorCodeRow{
	{api.CodeNotFound, "no. It means it does not exist or is not visible to you, deliberately indistinguishable"},
	{api.CodeUnauthenticated, "after `lt auth login`"},
	{api.CodeSessionRevoked, "no: the session was ended server-side. Run `lt auth login --force`"},
	{api.CodeOnboardingRequired, "no: the account has no LibreTimes profile yet; a person finishes onboarding"},
	{api.CodeForbidden, "no"},
	{api.CodeConflict, "usually no; read the message"},
	{api.CodeVenueRequired, "no: a book or a scientific work stays a draft until a venue publishes it. Set `is_draft: true` and re-run"},
	{api.CodeWorkIncomplete, "yes, after adding the fields its type requires; a per-file result lists their keys in `missing`"},
	{api.CodeFamilyChange, "no: a published work keeps its family. Keep its `type`, or publish under another `import_key`"},
	{api.CodeValidation, "yes, after correcting the named field"},
	{api.CodeRateLimited, "yes, after a pause"},
	{api.CodeUpstream, "yes, later"},
	{api.CodeTimeout, "yes, once"},
	{api.CodeNetwork, "yes, once"},
	{api.CodeHTTP, "a status lt has no mapping for; read the message"},
	{"blocked", "no: a deleted work holds this `import_key`. Restoring it is a person's decision"},
	{"not_a_book", "no: the key belongs to a work that is not a book; use another key"},
	{"no_expression", "no: the book has no text in that language; add the translation on the site"},
	{"unlisted_chapters", "after adding the chapters to `contents`, or with `--prune`"},
	{"unsupported_server", "no: the API predates collection import keys; wait for the platform"},
	{"bad_arguments", "no: lt refused the invocation before any request"},
	{"needs_confirmation", "with `--yes`"},
	{"not_signed_in", "after `lt auth login`"},
	{"local_error", "after fixing the file (unreadable, or malformed YAML); nothing was sent"},
	{"api_error", "read the message"},
}

func exitCodesTopic() string {
	var b strings.Builder
	b.WriteString("Exit codes, error codes, and what lt prints when something fails.\n\n" +
		"Results go to stdout; progress and errors go to stderr. With `--json`, a " +
		"failure prints an envelope on stderr and nothing on stdout, so a `| jq` " +
		"pipeline never finds an error where a result was promised:\n\n" +
		"```json\n{\n  \"error\": {\n    \"code\": \"session_revoked\",\n    \"message\": \"...\"\n  }\n}\n```\n\n" +
		"Commands that work through several files (`publish`, `course publish`, " +
		"`book publish`) report one result per file on stdout instead, each with its " +
		"`action` and, on a failure, the same `code` and a `message`. The run exits 1 " +
		"if any file failed; the others are still written.\n\n" +
		"## Exit codes\n\n| Code | Meaning | What to do |\n|---|---|---|\n")
	for _, r := range exitCodeRows {
		fmt.Fprintf(&b, "| %d | %s | %s |\n", r.code, r.meaning, r.action)
	}
	b.WriteString("\n## Error codes\n\nStable: branch on the code, never on the message.\n\n" +
		"| Code | Retry? |\n|---|---|\n")
	for _, r := range errorCodeRows {
		fmt.Fprintf(&b, "| `%s` | %s |\n", r.code, r.retry)
	}
	return strings.TrimRight(b.String(), "\n")
}

const environmentTopic = "The environment variables lt reads, and the files it keeps.\n\n" +
	"A flag wins over its variable, and the variable over the default.\n\n" +
	"## Variables\n\n" +
	"| Variable | Flag | What it does |\n|---|---|---|\n" +
	"| `LT_ENV` | `--env` | The deployment: `prod` (the default) or `dev`, a local stack behind its proxy. Sets the API, Keycloak, realm and client together |\n" +
	"| `LIBRETIMES_API_BASE_URL` | `--api-url` | The API base URL, e.g. `https://api.libretimes.io/libretimes/v1` |\n" +
	"| `LIBRETIMES_AUTH_URL` | `--auth-url` | The Keycloak base URL, without a realm path |\n" +
	"| `LIBRETIMES_REALM` | `--realm` | The Keycloak realm |\n" +
	"| `LIBRETIMES_CLIENT_ID` | `--client-id` | The Keycloak client id |\n" +
	"| `LIBRETIMES_TOKEN` | | An access token to use instead of the cache, for one run. Treat it as a secret |\n" +
	"| `LT_TIMEOUT_SECONDS` | | Scales every network timeout together, for a slow link. The overall budget, in seconds (default 180) |\n" +
	"| `XDG_CONFIG_HOME` | | On Linux and other Unix systems, where the config directory is (default `~/.config`) |\n\n" +
	"The installer, `install.sh`, reads three more: `LT_VERSION` pins a release, " +
	"`LT_INSTALL_DIR` chooses where the binary goes, and `LT_SKIP_CHECKSUM=1` " +
	"skips the checksum check (do not, unless you verified the archive yourself).\n\n" +
	"## Files\n\n" +
	"lt keeps one file: its credential cache, `credentials.json` in a `libretimes` " +
	"directory under your config directory, at mode 0600.\n\n" +
	"| System | Path |\n|---|---|\n" +
	"| Linux | `~/.config/libretimes/credentials.json` (or under `$XDG_CONFIG_HOME`) |\n" +
	"| macOS | `~/Library/Application Support/libretimes/credentials.json` |\n" +
	"| Windows | `%AppData%\\libretimes\\credentials.json` |\n\n" +
	"It holds one credential per deployment and client, so a dev login and a " +
	"production login live side by side. Only the refresh token lasts; access " +
	"tokens are short-lived and renewed as needed. There is no other " +
	"configuration file."

// fieldValues are the permitted values lt itself checks, per schema and key,
// named from the lists the validation uses so the page cannot drift from it.
func fieldValues(schema, key string) []string {
	switch {
	case key == "visibility" && schema == "CourseMeta":
		return api.CollectionVisibilityValues
	case key == "visibility":
		return api.VisibilityValues
	case key == "ai_usage":
		return api.AIUsageValues
	case key == "role" && schema == "Contributor":
		return api.ContributorRoleValues
	case key == "kind" && (schema == "ContentEntry" || schema == "ChapterMeta"):
		return api.DivisionKinds
	}
	return nil
}

func frontmatterTopic() string {
	var b strings.Builder
	b.WriteString("The keys lt reads from the YAML block at the top of a file.\n\n" +
		"The frontmatter is the manifest: there is no second file mapping files to " +
		"works. Keys lt does not know are ignored, so a file may carry frontmatter " +
		"for other tools. A list may be written as a YAML list or as comma-separated " +
		"text (`tags: a, b, c`).\n\n" +
		"```yaml\n---\ntitle: Integrals with a logarithm\n" +
		"import_key: reference/en/integrals-logarithmic\ntype: reference\nlanguage: en\n" +
		"license: cc_by_sa\nis_draft: true\ntags: [integrals, reference]\n" +
		"contributors:\n  - profile: \"@ivanov\"\n    role: lecturer\n---\n" +
		"The text of the work.\n```\n")
	for _, schema := range frontmatter.Schemas() {
		fmt.Fprintf(&b, "\n## %s\n\n%s\n\n| Key | Type | What it is |\n|---|---|---|\n",
			schema.Title, schema.Intro)
		for _, f := range schema.Fields {
			desc := f.Description
			if values := fieldValues(schema.Name, f.Key); len(values) > 0 {
				desc += " One of: `" + strings.Join(values, "`, `") + "`."
			}
			fmt.Fprintf(&b, "| `%s` | %s | %s |\n", f.Key, f.Type, desc)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
