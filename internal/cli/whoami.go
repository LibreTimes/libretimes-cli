package cli

import "github.com/spf13/cobra"

func newWhoamiCommand(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show the profile the current token belongs to",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return showMe(cmd.Context(), e, false)
		},
	}
}
