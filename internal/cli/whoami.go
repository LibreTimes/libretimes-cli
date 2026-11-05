package cli

import "github.com/spf13/cobra"

func newWhoamiCommand(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show the profile the current token belongs to",
		Long: "Show the profile the current credential acts as: its name, handle and " +
			"profile_id.\n\n" +
			"Every write lt makes is made as this profile, so check it before an " +
			"import when more than one account has signed in on this machine. Reads " +
			"only; it refreshes the access token if it has expired.",
		Example: "  # Who am I?\n" +
			"  lt whoami\n\n" +
			"  # The handle alone, for a script\n" +
			"  lt whoami --json | jq -r .username",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return showMe(cmd.Context(), e, false)
		},
	}
}
