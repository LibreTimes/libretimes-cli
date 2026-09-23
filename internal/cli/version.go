package cli

import (
	"fmt"
	"io"
	"runtime"

	"github.com/spf13/cobra"
)

// Build information, set by the linker at release time:
//
//	-X github.com/The-LibreTimes/libretimes-cli/internal/cli.Version=v1.2.3
//
// The default is what a `go build` with no ldflags produces, and saying "dev"
// out loud is better than reporting a version that was never released.
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

type versionInfo struct {
	Version  string `json:"version"`
	Commit   string `json:"commit"`
	Date     string `json:"date"`
	Go       string `json:"go"`
	Platform string `json:"platform"`
}

func newVersionCommand(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show the lt version",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			info := versionInfo{
				Version:  Version,
				Commit:   Commit,
				Date:     Date,
				Go:       runtime.Version(),
				Platform: runtime.GOOS + "/" + runtime.GOARCH,
			}
			return e.emit(info, func(w io.Writer) {
				fmt.Fprintf(w, "lt %s (%s, %s)\n", info.Version, info.Commit, info.Date)
				fmt.Fprintf(w, "%s %s\n", info.Go, info.Platform)
			})
		},
	}
}
