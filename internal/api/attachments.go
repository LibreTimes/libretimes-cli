package api

import (
	"bytes"
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
)

// Attachment is a file bound to a publication, as the publication read reports
// it. DownloadURL is a short-lived signed URL on the storage host, absent until
// the file has cleared scanning.
type Attachment struct {
	ID           string `json:"id"`
	OriginalName string `json:"original_name"`
	DisplayName  string `json:"display_name,omitempty"`
	ContentType  string `json:"content_type,omitempty"`
	SizeBytes    int64  `json:"size_bytes"`
	Status       string `json:"status"`
	DownloadURL  string `json:"download_url,omitempty"`
}

// uploadTicket is what request-upload hands back: where to POST the bytes, the
// signed policy fields that must ride with them, and the token that confirms
// the upload afterwards.
type uploadTicket struct {
	AttachmentID string            `json:"attachment_id"`
	UploadURL    string            `json:"upload_url"`
	UploadFields map[string]string `json:"upload_fields"`
	UploadToken  string            `json:"upload_token"`
}

// UploadAttachment stores one file and returns it, not yet bound to anything.
//
// Three steps, the same the site's editor takes: request a ticket, POST the
// bytes straight to object storage under the signed policy, confirm. The bytes
// never pass through the API. Binding the file to a publication is a separate
// write on the publication (SetAttachmentIDs), so a failed bind leaves an
// unbound upload the storage service expires, never a half-bound one.
func (c *Client) UploadAttachment(ctx context.Context, name, contentType string, data []byte) (*Attachment, error) {
	var ticket uploadTicket
	body := map[string]any{"filename": name, "size_bytes": len(data)}
	if contentType != "" {
		body["content_type"] = contentType
	}
	if err := c.Post(ctx, "/attachments/request-upload", body, &ticket); err != nil {
		return nil, err
	}
	if err := c.postToStorage(ctx, ticket, name, contentType, data); err != nil {
		return nil, err
	}
	var out Attachment
	err := c.Post(ctx, "/attachments/"+url.PathEscape(ticket.AttachmentID)+"/confirm",
		map[string]string{"upload_token": ticket.UploadToken}, &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// postToStorage sends the multipart form a presigned POST expects: the policy
// fields first, the file last (S3 ignores every field after `file`).
func (c *Client) postToStorage(ctx context.Context, ticket uploadTicket, name, contentType string, data []byte) error {
	var buf bytes.Buffer
	form := multipart.NewWriter(&buf)
	for k, v := range ticket.UploadFields {
		if err := form.WriteField(k, v); err != nil {
			return err
		}
	}
	part, err := form.CreateFormFile("file", name)
	if err != nil {
		return err
	}
	if _, err := part.Write(data); err != nil {
		return err
	}
	if err := form.Close(); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ticket.UploadURL, &buf)
	if err != nil {
		return &Error{CodeUpstream, "The API returned an upload URL lt could not use."}
	}
	req.Header.Set("Content-Type", form.FormDataContentType())
	resp, err := c.storage.Do(req)
	if err != nil {
		return &Error{CodeNetwork, "Could not reach the storage host to upload the file."}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		// The storage host's error body is S3 XML about a policy lt did not
		// write; the status is the useful part.
		return &Error{CodeUpstream, "The storage host refused the upload (HTTP " +
			http.StatusText(resp.StatusCode) + ")."}
	}
	return nil
}

// DownloadAttachment fetches a file's bytes through its signed download URL.
// No token is sent: the URL carries its own signature.
func (c *Client) DownloadAttachment(ctx context.Context, a Attachment) ([]byte, error) {
	if a.DownloadURL == "" {
		return nil, &Error{CodeUpstream, "The file has no download URL yet (it may still be scanning)."}
	}
	target := a.DownloadURL
	if strings.HasPrefix(target, "/") {
		// A relative URL is an API path; the storage client would send it
		// nowhere. Not expected today, handled so a change there fails loud.
		return nil, &Error{CodeUpstream, "The API returned a relative download URL lt cannot follow."}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, &Error{CodeUpstream, "The API returned a download URL lt could not use."}
	}
	resp, err := c.storage.Do(req)
	if err != nil {
		return nil, &Error{CodeNetwork, "Could not reach the storage host to read the file."}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &Error{CodeUpstream, "The storage host refused the download (HTTP " +
			http.StatusText(resp.StatusCode) + ")."}
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAttachmentBytes+1))
	if err != nil {
		return nil, &Error{CodeNetwork, "The connection dropped while reading the file."}
	}
	return data, nil
}

// maxAttachmentBytes bounds what lt will read back to compare. Well above the
// platform's per-file limit, so it only ever stops a runaway response.
const maxAttachmentBytes = 256 << 20
