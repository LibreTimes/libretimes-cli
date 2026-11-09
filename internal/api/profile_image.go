package api

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
)

// ProfileImage is the caller's profile picture. Width and height are set on
// upload only: the stored size, so a caller can see what its crop became.
type ProfileImage struct {
	ProfileImageURL *string `json:"profile_image_url"`
	Width           int     `json:"width,omitempty"`
	Height          int     `json:"height,omitempty"`
}

// ImageCrop is how the server makes the square. Crop ("x,y,w,h", pixels of
// the upright image) wins over Focus ("x,y", each -1..1 from the centre, y
// up); neither means a centred square. The server does the cropping, the
// re-encoding to WebP and the stripping of EXIF and ICC data -- lt sends the
// original bytes untouched.
type ImageCrop struct {
	Crop  string
	Focus string
}

// ProfileImage returns the caller's current picture.
func (c *Client) ProfileImage(ctx context.Context) (*ProfileImage, error) {
	var img ProfileImage
	if err := c.Get(ctx, "/account/profile-image", nil, &img); err != nil {
		return nil, err
	}
	return &img, nil
}

// SetProfileImage uploads a picture for the caller's own profile, replacing
// any picture it had.
func (c *Client) SetProfileImage(ctx context.Context, name, contentType string, data []byte,
	crop ImageCrop) (*ProfileImage, error) {
	var buf bytes.Buffer
	form := multipart.NewWriter(&buf)
	for field, value := range map[string]string{"crop": crop.Crop, "focus": crop.Focus} {
		if value == "" {
			continue
		}
		if err := form.WriteField(field, value); err != nil {
			return nil, fmt.Errorf("encoding form: %w", err)
		}
	}
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename=%q`, name))
	header.Set("Content-Type", contentType)
	part, err := form.CreatePart(header)
	if err != nil {
		return nil, fmt.Errorf("encoding form: %w", err)
	}
	if _, err := part.Write(data); err != nil {
		return nil, fmt.Errorf("encoding form: %w", err)
	}
	if err := form.Close(); err != nil {
		return nil, fmt.Errorf("encoding form: %w", err)
	}

	var img ProfileImage
	if err := c.send(ctx, http.MethodPost, "/account/profile-image", nil,
		form.FormDataContentType(), &buf, &img); err != nil {
		return nil, err
	}
	return &img, nil
}

// RemoveProfileImage deletes the caller's picture.
func (c *Client) RemoveProfileImage(ctx context.Context) error {
	return c.Delete(ctx, "/account/profile-image")
}
