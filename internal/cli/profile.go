package cli

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/The-LibreTimes/libretimes-cli/internal/api"
	"github.com/The-LibreTimes/libretimes-cli/internal/config"
)

// The profile picture of the signed-in account. lt sends the original file;
// the server checks it, crops it to a square, re-encodes it as WebP and drops
// its metadata (EXIF, GPS, ICC). The checks here only spare a round trip for
// a file the server would refuse anyway -- the server's are the real ones.

// maxImageBytes is the server's upload cap.
const maxImageBytes = 5 << 20

// imageTypes are the formats the server decodes, by sniffed content type.
var imageTypes = map[string]bool{
	"image/jpeg": true, "image/png": true, "image/webp": true, "image/gif": true, "image/avif": true,
}

// profileImageResult is a command's outcome.
type profileImageResult struct {
	Action          string  `json:"action"`
	Path            string  `json:"path,omitempty"`
	ProfileImageURL *string `json:"profile_image_url"`
	Width           int     `json:"width,omitempty"`
	Height          int     `json:"height,omitempty"`
}

func newProfileCommand(e *env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Your own profile",
	}
	image := &cobra.Command{
		Use:   "image",
		Short: "Show, set or remove your profile picture",
	}
	image.AddCommand(newProfileImageShowCommand(e), newProfileImageSetCommand(e), newProfileImageRmCommand(e))
	cmd.AddCommand(image)
	return cmd
}

func newProfileImageShowCommand(e *env) *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Print the URL of your profile picture",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, _, err := e.client(cmd.Context(), true, config.ProductLibreTimes)
			if err != nil {
				return err
			}
			img, err := client.ProfileImage(cmd.Context())
			if err != nil {
				return &exitError{code: ExitAPIError, err: err}
			}
			return e.emit(profileImageResult{Action: "show", ProfileImageURL: img.ProfileImageURL},
				func(w io.Writer) {
					if img.ProfileImageURL == nil {
						fmt.Fprintln(w, "no profile picture")
						return
					}
					fmt.Fprintln(w, *img.ProfileImageURL)
				})
		},
	}
}

func newProfileImageSetCommand(e *env) *cobra.Command {
	var dryRun, yes bool
	var crop api.ImageCrop
	cmd := &cobra.Command{
		Use:   "set <file>",
		Short: "Upload a profile picture, replacing the current one",
		Long: "Upload a JPEG, PNG, WebP, GIF or AVIF of at most 5 MB as your profile picture. " +
			"It is stored as a square WebP of at most 512 px, with its metadata (EXIF, GPS, " +
			"colour profile) removed.\n\n" +
			"The server makes the square: --crop \"x,y,w,h\" in pixels of the upright image, " +
			"or --focus \"x,y\" (each -1..1 from the centre, y up) to keep a point in frame, " +
			"or neither for a centred square.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if crop.Crop != "" && crop.Focus != "" {
				return fail(ExitBadArguments, "give --crop or --focus, not both")
			}
			path := args[0]
			data, contentType, err := readImage(path)
			if err != nil {
				return err
			}
			if err := needsConfirm(dryRun, yes, "profile image set would replace your profile picture"); err != nil {
				return err
			}
			result := profileImageResult{Action: "set", Path: path}
			if dryRun {
				result.Action = "plan"
				return e.emit(result, func(w io.Writer) {
					fmt.Fprintf(w, "would upload %s (%s, %d bytes) as your profile picture\n",
						filepath.Base(path), contentType, len(data))
				})
			}
			client, _, err := e.client(cmd.Context(), true, config.ProductLibreTimes)
			if err != nil {
				return err
			}
			img, err := client.SetProfileImage(cmd.Context(), filepath.Base(path), contentType, data, crop)
			if err != nil {
				return &exitError{code: ExitAPIError, err: err}
			}
			result.ProfileImageURL, result.Width, result.Height = img.ProfileImageURL, img.Width, img.Height
			return e.emit(result, func(w io.Writer) {
				if img.ProfileImageURL != nil {
					fmt.Fprintf(w, "%s (%dx%d)\n", *img.ProfileImageURL, img.Width, img.Height)
				}
			})
		},
	}
	cmd.Flags().StringVar(&crop.Crop, "crop", "", `The square to keep: "x,y,w,h" in pixels.`)
	cmd.Flags().StringVar(&crop.Focus, "focus", "", `A point to keep in frame: "x,y", each -1..1, y up.`)
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Check the file; upload nothing.")
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm the upload.")
	return cmd
}

func newProfileImageRmCommand(e *env) *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "rm",
		Short: "Remove your profile picture",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := needsConfirm(false, yes, "profile image rm would remove your profile picture"); err != nil {
				return err
			}
			client, _, err := e.client(cmd.Context(), true, config.ProductLibreTimes)
			if err != nil {
				return err
			}
			if err := client.RemoveProfileImage(cmd.Context()); err != nil {
				return &exitError{code: ExitAPIError, err: err}
			}
			return e.emit(profileImageResult{Action: "remove"}, func(w io.Writer) {
				fmt.Fprintln(w, "profile picture removed")
			})
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm the removal.")
	return cmd
}

// readImage reads a picture and names its type from its bytes, not its
// extension: a .jpg that is really a PNG is still a PNG to the server.
func readImage(path string) ([]byte, string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, "", fail(ExitBadArguments, "%s: %v", path, err)
	}
	if info.Size() > maxImageBytes {
		return nil, "", fail(ExitBadArguments, "%s is %d bytes; the limit is 5 MB", path, info.Size())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fail(ExitBadArguments, "%s: %v", path, err)
	}
	contentType := http.DetectContentType(data)
	if strings.HasPrefix(contentType, "application/octet-stream") && isAVIF(data) {
		contentType = "image/avif"
	}
	if !imageTypes[contentType] {
		return nil, "", fail(ExitBadArguments, "%s is %s; a profile picture is JPEG, PNG, WebP, GIF or AVIF",
			path, contentType)
	}
	return data, contentType, nil
}

// isAVIF recognises the ISO-BMFF brand net/http does not sniff.
func isAVIF(data []byte) bool {
	return len(data) >= 12 && string(data[4:8]) == "ftyp" &&
		(string(data[8:12]) == "avif" || string(data[8:12]) == "avis")
}
