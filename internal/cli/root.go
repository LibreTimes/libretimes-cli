// Package cli wires the command surface.
//
// lt is gh-shaped: named subcommands with named flags, not a generic verb
// dispatcher. `gh pr create --title X`, never `gh call create_pull_request
// --arg title=X`.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/The-LibreTimes/libretimes-cli/internal/api"
	"github.com/The-LibreTimes/libretimes-cli/internal/auth"
	"github.com/The-LibreTimes/libretimes-cli/internal/config"
)

// Exit codes. Fixed and documented, because agents branch on them; they match
// the Python implementation this replaces, so anything already scripted keeps
// working.
const (
	ExitOK           = 0
	ExitAPIError     = 1 // the API refused the request
	ExitBadArguments = 2 // bad arguments, or a missing required field
	ExitNeedsConfirm = 3 // a write was attempted without --yes
	ExitNotSignedIn  = 4 // not signed in, or the credential cache is unusable
)

// exitError carries an exit code up to main without printing anything itself.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

func fail(code int, format string, args ...any) error {
	return &exitError{code: code, err: fmt.Errorf(format, args...)}
}

// env is everything a command needs, resolved once on the root.
type env struct {
	overrides config.Overrides
	jsonOut   bool
	quiet     bool

	out io.Writer
	err io.Writer
}

func (e *env) profile(product string) (config.Profile, error) {
	p, err := config.Resolve(e.overrides, product)
	if err != nil {
		return config.Profile{}, &exitError{code: ExitBadArguments, err: err}
	}
	return p, nil
}

func (e *env) store() (*auth.Store, error) {
	s, err := auth.NewStore()
	if err != nil {
		return nil, &exitError{code: ExitNotSignedIn, err: err}
	}
	return s, nil
}

// client builds an API client for one product. When required is false a
// missing credential is not an error — reads of public data work anonymously.
func (e *env) client(ctx context.Context, required bool, product string) (*api.Client, config.Profile, error) {
	profile, err := e.profile(product)
	if err != nil {
		return nil, config.Profile{}, err
	}
	store, err := e.store()
	if err != nil {
		return nil, config.Profile{}, err
	}

	token, err := auth.AccessToken(ctx, store, profile)
	if err != nil {
		if !errors.Is(err, auth.ErrNotSignedIn) {
			return nil, config.Profile{}, &exitError{code: ExitNotSignedIn, err: err}
		}
		if required {
			return nil, config.Profile{}, &exitError{
				code: ExitNotSignedIn,
				err: errors.New(
					"not signed in. Run `lt auth login` to authenticate through your " +
						"browser, or `lt auth login --device` if this machine has no " +
						"browser. (LIBRETIMES_TOKEN overrides the cache for a one-off.)"),
			}
		}
	}

	return api.New(profile.APIBase, token, Version), profile, nil
}

// emit writes a value as JSON when --json is set, otherwise runs the
// human-readable renderer.
func (e *env) emit(value any, human func(io.Writer)) error {
	if e.jsonOut {
		encoder := json.NewEncoder(e.out)
		encoder.SetIndent("", "  ")
		// Wire format is snake_case end to end; the structs carry explicit
		// json tags, so this needs no reshaping.
		return encoder.Encode(value)
	}
	human(e.out)
	return nil
}

// info writes progress to stderr, so stdout stays parseable. Suppressed by
// --quiet.
func (e *env) info(format string, args ...any) {
	if e.quiet {
		return
	}
	fmt.Fprintf(e.err, format+"\n", args...)
}

// NewRootCommand builds the command tree, and returns the env alongside it.
//
// Execute needs the env, not just the command: whether a failure is rendered
// as prose or as JSON is a --json decision, and --json is parsed into the env
// by cobra. Without this the error path could not honour the flag, which is
// how `--json` came to emit no JSON at all when a command failed.
func NewRootCommand() (*cobra.Command, *env) {
	e := &env{out: os.Stdout, err: os.Stderr}

	root := &cobra.Command{
		Use:   "lt",
		Short: "Work with LibreTimes from the command line",
		Long: "lt is a client of the LibreTimes public API.\n\n" +
			"It holds no internal credential and reaches nothing a third party could " +
			"not reach with the same token.",
		SilenceUsage:  true,
		SilenceErrors: true,
		// A bare `lt` should show help, not an error.
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	flags := root.PersistentFlags()
	flags.StringVar(&e.overrides.Env, "env", "",
		"Deployment to talk to: prod or dev. Defaults to $LT_ENV, then prod.")
	flags.StringVar(&e.overrides.APIBase, "api-url", "",
		"Override the API base URL. Defaults to $LIBRETIMES_API_BASE_URL.")
	flags.StringVar(&e.overrides.AuthURL, "auth-url", "",
		"Override the Keycloak base URL. Defaults to $LIBRETIMES_AUTH_URL.")
	flags.StringVar(&e.overrides.Realm, "realm", "",
		"Override the Keycloak realm. Defaults to $LIBRETIMES_REALM.")
	flags.StringVar(&e.overrides.ClientID, "client-id", "",
		"Override the Keycloak client id. Defaults to $LIBRETIMES_CLIENT_ID.")
	flags.BoolVar(&e.jsonOut, "json", false, "Emit machine-readable JSON on stdout.")
	flags.BoolVarP(&e.quiet, "quiet", "q", false, "Suppress progress output; keep errors.")

	root.AddCommand(
		newAuthCommand(e),
		newWhoamiCommand(e),
		newPublishCommand(e),
		newCourseCommand(e),
		newBookCommand(e),
		newVersionCommand(e),
	)
	return root, e
}

// Execute runs the CLI and returns the process exit code.
//
// ctx carries signal cancellation from main, so an interrupted publish stops
// its in-flight request rather than being killed mid-write.
func Execute(ctx context.Context) int {
	root, e := NewRootCommand()
	err := root.ExecuteContext(ctx)
	if err == nil {
		return ExitOK
	}

	code, message, exit := classify(err)

	// errQuiet: publish already reported each file, so there is nothing to add.
	// Adding a second, vaguer line under the specific ones is worse than
	// silence, and in --json mode the results array already carries the codes.
	if message != "" {
		if e.jsonOut {
			encoder := json.NewEncoder(e.err)
			encoder.SetIndent("", "  ")
			_ = encoder.Encode(errorEnvelope{Error: errorBody{Code: code, Message: message}})
		} else {
			fmt.Fprintln(e.err, message)
		}
	}
	return exit
}

// errorEnvelope is the --json failure shape.
//
// It goes to stderr, not stdout. stdout carries results, and a consumer piping
// `lt ... --json | jq` should never find an error object where the command's
// documented shape was promised — the exit code is what says "look at stderr".
type errorEnvelope struct {
	Error errorBody `json:"error"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// classify reduces any error to (code, message, exit code).
//
// The api.Error code is preferred wherever there is one, because that is the
// vocabulary AGENTS.md documents and agents branch on. A failure that never
// reached the API gets a slug naming the CLI-level reason instead.
func classify(err error) (code, message string, exit int) {
	var apiErr *api.Error
	hasAPI := errors.As(err, &apiErr)

	var ee *exitError
	if errors.As(err, &ee) {
		if errors.As(ee.err, &errQuietTarget) {
			return "", "", ee.code
		}
		if hasAPI {
			return apiErr.Code, apiErr.Message, ee.code
		}
		return slugFor(ee.code), ee.err.Error(), ee.code
	}

	// An *api.Error that escaped without an explicit exit code: the API
	// refused the request.
	if hasAPI {
		return apiErr.Code, apiErr.Message, ExitAPIError
	}

	// Anything left is cobra's own argument handling.
	return slugFor(ExitBadArguments), err.Error(), ExitBadArguments
}

// errQuietTarget exists only as an errors.As target; publish's errQuiet is a
// value type carrying nothing.
var errQuietTarget errQuiet

func slugFor(exit int) string {
	switch exit {
	case ExitBadArguments:
		return "bad_arguments"
	case ExitNeedsConfirm:
		return "needs_confirmation"
	case ExitNotSignedIn:
		return "not_signed_in"
	default:
		return "api_error"
	}
}
