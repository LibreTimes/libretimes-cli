package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/The-LibreTimes/libretimes-cli/internal/auth"
	"github.com/The-LibreTimes/libretimes-cli/internal/config"
)

func newAuthCommand(e *env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth <command>",
		Short: "Sign in to LibreTimes and manage cached credentials",
	}
	cmd.AddCommand(
		newAuthLoginCommand(e),
		newAuthLogoutCommand(e),
		newAuthStatusCommand(e),
		newAuthTokenCommand(e),
	)
	return cmd
}

func newAuthLoginCommand(e *env) *cobra.Command {
	var (
		device bool
		force  bool
		scopes []string
	)

	cmd := &cobra.Command{
		Use:   "login",
		Short: "Sign in through the browser (OAuth 2.1 + PKCE)",
		Long: "Sign in to LibreTimes.\n\n" +
			"By default this opens your browser and catches the redirect on a " +
			"loopback port. Use --device where that cannot work — a container, an " +
			"SSH session, a CI runner — and authorize on another machine instead.\n\n" +
			"lt never sees your password. The browser and Keycloak handle it, which " +
			"is what keeps MFA, session revocation and account state working.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()

			// Auth (issuer, realm, client id) never varies by product, so
			// the product argument only matters if something downstream
			// reads profile.APIBase -- nothing here does.
			profile, err := e.profile(config.ProductLibreTimes)
			if err != nil {
				return err
			}
			store, err := e.store()
			if err != nil {
				return err
			}

			requested := append(append([]string{}, auth.DefaultScopes...), scopes...)

			var cred *auth.Credential
			if device {
				cred, err = auth.LoginDevice(ctx, profile, requested, force, e.err)
			} else {
				cred, err = auth.LoginBrowser(ctx, profile, requested, force, e.err)
			}
			if err != nil {
				return &exitError{code: ExitAPIError, err: err}
			}

			if err := store.Save(cred); err != nil {
				return &exitError{code: ExitNotSignedIn, err: err}
			}

			e.info("\nSigned in at %s", profile.Issuer())
			e.info("Credentials cached in %s", store.Path())

			// Confirm the token actually works against the API rather than
			// just reporting that Keycloak minted one. The two can disagree —
			// a token with the wrong audience is issued happily and refused at
			// the first request — and finding that out now beats finding out
			// on the user's first real command.
			return showMe(ctx, e, true)
		},
	}

	cmd.Flags().BoolVar(&device, "device", false,
		"Use the device authorization grant: print a code to enter on another device.")
	cmd.Flags().BoolVar(&force, "force", false,
		"Re-authenticate instead of reusing the browser's existing session.\n"+
			"Needed after a server-side sign-out (session_revoked): a refresh and an\n"+
			"ordinary login both keep the same Keycloak session, which is the thing\n"+
			"that was revoked.")
	cmd.Flags().StringArrayVar(&scopes, "scope", nil,
		"Additional scope to request, e.g. --scope cv:write. Repeatable.\n"+
			"Note: the REST API authorizes on identity, not on the scope claim, so "+
			"this is a statement of intent rather than a permission boundary.")
	return cmd
}

func newAuthLogoutCommand(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Forget the cached credentials for this deployment",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			profile, err := e.profile(config.ProductLibreTimes)
			if err != nil {
				return err
			}
			store, err := e.store()
			if err != nil {
				return err
			}

			removed, err := store.Delete(profile.Issuer(), profile.ClientID)
			if err != nil {
				return &exitError{code: ExitNotSignedIn, err: err}
			}
			if !removed {
				e.info("Not signed in at %s.", profile.Issuer())
				return nil
			}
			e.info("Removed the cached credentials for %s.", profile.Issuer())
			// Deliberately local-only. Ending the Keycloak *session* is a
			// different act from forgetting this machine's copy of it, and
			// silently doing both would sign the user out of their browser too.
			e.info("The Keycloak session itself is untouched; sign out there to end it.")
			return nil
		},
	}
}

// statusEntry is one cached login, as `--json` reports it.
type statusEntry struct {
	Issuer string `json:"issuer"`
	// Username is which account this credential belongs to. The store holds
	// one credential per issuer+client, so signing in as a second account
	// silently replaces the first — and `publish` writes as whoever holds the
	// token. Without this, nothing short of `lt whoami` tells you that the
	// account changed, and by then an import may have gone to the wrong one.
	Username  string `json:"username,omitempty"`
	ClientID  string `json:"client_id"`
	ExpiresAt string `json:"expires_at"`
	Expired   bool   `json:"expired"`
	Active    bool   `json:"active"`
}

func newAuthStatusCommand(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show which deployments you are signed in to",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			profile, err := e.profile(config.ProductLibreTimes)
			if err != nil {
				return err
			}
			store, err := e.store()
			if err != nil {
				return err
			}
			creds, err := store.All()
			if err != nil {
				return &exitError{code: ExitNotSignedIn, err: err}
			}

			entries := make([]statusEntry, 0, len(creds))
			for _, c := range creds {
				entries = append(entries, statusEntry{
					Issuer:    c.Issuer,
					Username:  auth.UsernameFromToken(c.AccessToken),
					ClientID:  c.ClientID,
					ExpiresAt: c.Expiry().UTC().Format(time.RFC3339),
					Expired:   time.Now().After(c.Expiry()),
					Active:    c.Issuer == profile.Issuer() && c.ClientID == profile.ClientID,
				})
			}

			return e.emit(entries, func(w io.Writer) {
				if len(entries) == 0 {
					fmt.Fprintf(w, "Not signed in anywhere.\nRun `lt auth login` to start.\n")
					return
				}
				for _, entry := range entries {
					marker := " "
					if entry.Active {
						marker = "*"
					}
					state := "access token valid until " + entry.ExpiresAt
					if entry.Expired {
						// Not the same as signed out: the refresh token
						// outlives the access token, so the next command
						// silently buys a new one.
						state = "access token expired; will refresh on next use"
					}
					fmt.Fprintf(w, "%s %s\n", marker, entry.Issuer)
					if entry.Username != "" {
						fmt.Fprintf(w, "    signed in as @%s\n", entry.Username)
					}
					fmt.Fprintf(w, "    client %s, %s\n", entry.ClientID, state)
				}
				fmt.Fprintf(w, "\n* = the deployment this invocation would use.\n")
			})
		},
	}
}

func newAuthTokenCommand(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "token",
		Short: "Print a valid access token for scripting",
		Long: "Print a valid access token on stdout, refreshing it if needed.\n\n" +
			"This is the one command that emits a credential, and it does so only " +
			"because you asked. Treat the output as a secret: it authenticates as " +
			"you until it expires.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx := cmd.Context()
			profile, err := e.profile(config.ProductLibreTimes)
			if err != nil {
				return err
			}
			store, err := e.store()
			if err != nil {
				return err
			}

			token, err := auth.AccessToken(ctx, store, profile)
			if err != nil {
				if errors.Is(err, auth.ErrNotSignedIn) {
					return fail(ExitNotSignedIn,
						"not signed in. Run `lt auth login` first.")
				}
				return &exitError{code: ExitNotSignedIn, err: err}
			}
			fmt.Fprintln(e.out, token)
			return nil
		},
	}
}

// showMe fetches and prints the caller's profile. Shared by `auth login` and
// `whoami` so a successful login proves the token end to end.
func showMe(ctx context.Context, e *env, afterLogin bool) error {
	// profiles/me is mounted on every BFF from the shared
	// libretimes-account-api, and answers the same way regardless of which
	// one is asked -- identity is not product-scoped -- so the product here
	// is arbitrary rather than significant.
	client, _, err := e.client(ctx, true, config.ProductLibreTimes)
	if err != nil {
		return err
	}

	profile, err := client.Me(ctx)
	if err != nil {
		return &exitError{code: ExitAPIError, err: err}
	}

	return e.emit(profile, func(w io.Writer) {
		if afterLogin {
			fmt.Fprintf(w, "\n")
		}
		fmt.Fprintf(w, "%s (@%s)\n", profile.Name(), profile.Username)
		fmt.Fprintf(w, "profile_id %s\n", profile.ProfileID)
	})
}
