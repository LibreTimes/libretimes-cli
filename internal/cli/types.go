package cli

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/The-LibreTimes/libretimes-cli/internal/api"
	"github.com/The-LibreTimes/libretimes-cli/internal/config"
)

// newTypesCommand lists the type registry: what a file's `type` may be.
func newTypesCommand(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "types [kind]",
		Short: "List the kinds of work, and what each needs before it is published",
		Long: "List the kinds of work LibreTimes hosts: the values a file's `type` may take.\n\n" +
			"For each kind it shows its family (a page, or a scientific work), who " +
			"publishes it, and the fields a publish refuses without. A kind published " +
			"by a venue -- books and scientific works -- stays a draft until a venue " +
			"publishes it: its file says `is_draft: true`, and a publish without that is " +
			"refused. Name a kind " +
			"to see everything the registry says about it.\n\n" +
			"The list is read from the API on every run, never from a copy in lt, so " +
			"it is always the server's. Reading it needs no sign-in and changes nothing.",
		Example: "  # Every kind, with its family and its required fields\n" +
			"  lt types\n\n" +
			"  # One kind in full\n" +
			"  lt types original_research\n\n" +
			"  # The kinds a venue publishes, for a script\n" +
			"  lt types --json | jq -r '.kinds[] | select(.publish == \"venue\") | .kind'",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, _, err := e.client(cmd.Context(), false, config.ProductLibreTimes)
			if err != nil {
				return err
			}
			types, err := client.WorkTypes(cmd.Context())
			if err != nil {
				return &exitError{code: ExitAPIError, err: err}
			}
			if len(args) == 0 {
				return e.emit(types, func(w io.Writer) { renderTypes(w, types) })
			}
			kind := types.Kind(args[0])
			if kind == nil {
				return fail(ExitBadArguments, "%q is not a kind of work. The kinds are: %s",
					args[0], strings.Join(types.KindNames(), ", "))
			}
			return e.emit(kind, func(w io.Writer) { renderKind(w, kind) })
		},
	}
}

func publisherOf(k api.KindType) string {
	if k.Publish == api.PublishVenue {
		return "a venue"
	}
	return "you"
}

func listOrDash(items []string) string {
	if len(items) == 0 {
		return "-"
	}
	return strings.Join(items, ", ")
}

func renderTypes(w io.Writer, types *api.WorkTypes) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "KIND\tFAMILY\tPUBLISHED BY\tREQUIRED")
	for _, k := range types.Kinds {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", k.Kind, k.Family, publisherOf(k), listOrDash(k.Required))
	}
	tw.Flush()
}

func renderKind(w io.Writer, k *api.KindType) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "kind\t%s\n", k.Kind)
	fmt.Fprintf(tw, "family\t%s\n", k.Family)
	fmt.Fprintf(tw, "published by\t%s\n", publisherOf(*k))
	fmt.Fprintf(tw, "strictness\t%s\n", k.Strictness)
	fmt.Fprintf(tw, "direction\t%s\n", k.Direction)
	fmt.Fprintf(tw, "lead\t%s, up to %d characters\n", k.Lead.Label, k.Lead.Max)
	fmt.Fprintf(tw, "required\t%s\n", listOrDash(k.Required))
	fmt.Fprintf(tw, "recommended\t%s\n", listOrDash(k.Recommended))
	fmt.Fprintf(tw, "significant edits\t%s\n", listOrDash(k.Significant))
	tw.Flush()
	if k.Publish == api.PublishVenue {
		fmt.Fprintln(w, "\nA venue publishes this kind: its file says `is_draft: true`, and lt refuses a publish without it.")
	}
}
