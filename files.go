package popnow

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// UploadTicket is a presigned slot for exactly one file of a declared size.
type UploadTicket struct {
	FileID string `json:"fileId"`
	// Key is a compatibility alias of the final URI and is unusable until the
	// commit succeeds. Put UploadedFile.URI in task input, never this.
	Key       string `json:"key"`
	UploadURL string `json:"uploadUrl"`
	Method    string `json:"method"`
	// Headers must be forwarded to the PUT exactly as given.
	Headers map[string]string `json:"headers"`
	// ExpiresAt covers the PUT and the commit together: about twenty minutes,
	// two hours for LoRA weights. Ask for a ticket right before uploading.
	ExpiresAt time.Time `json:"expiresAt"`
	// MaxBytes is the authoritative ceiling for this upload.
	MaxBytes int64 `json:"maxBytes"`
}

// UploadedFile is a committed file, ready to be referenced by a task.
type UploadedFile struct {
	FileID string `json:"fileId"`
	Status string `json:"status"`
	Bytes  int64  `json:"bytes"`
	// ContentType is what the server verified.
	ContentType string `json:"contentType"`
	SHA256      string `json:"sha256"`
	// URI is the popnow://f/fil_... value to put in task input (or in
	// loras[].path for LoRA weights). Use this one, not the upload URL, not a
	// storage key, not the bare file ID.
	URI       string    `json:"uri"`
	ExpiresAt time.Time `json:"expiresAt"`
	// DurationSeconds is a decimal string, present for audio and video.
	DurationSeconds string `json:"durationSeconds,omitempty"`
	Width           int    `json:"width,omitempty"`
	Height          int    `json:"height,omitempty"`
}

// DownloadTicket is a fresh signed link to one output of a finished task.
type DownloadTicket struct {
	Key       string    `json:"key"`
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// ContentTypeLoRA is the media type LoRA weights (.safetensors) upload under.
const ContentTypeLoRA = "application/octet-stream"

// uploadContentTypes maps an extension to one of the accepted media types.
//
// ".key" is left out on purpose although Keynote is accepted. It is also the
// usual extension of a PEM private key, and inferring a type from it would
// stream that key's bytes into object storage before the server's commit check
// got the chance to refuse them. Pass the content type explicitly to upload a
// Keynote file.
var uploadContentTypes = map[string]string{
	".gif":  "image/gif",
	".jpeg": "image/jpeg",
	".jpg":  "image/jpeg",
	".png":  "image/png",
	".webp": "image/webp",
	".mp4":  "video/mp4",
	".webm": "video/webm",
	".mp3":  "audio/mpeg",
	".wav":  "audio/wav",

	".pdf":     "application/pdf",
	".doc":     "application/msword",
	".docx":    "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	".xls":     "application/vnd.ms-excel",
	".xlsx":    "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	".ppt":     "application/vnd.ms-powerpoint",
	".pptx":    "application/vnd.openxmlformats-officedocument.presentationml.presentation",
	".pages":   "application/vnd.apple.pages",
	".numbers": "application/vnd.apple.numbers",
	".txt":     "text/plain",
	".md":      "text/markdown",

	".safetensors": ContentTypeLoRA,
}

// acceptedUploadTypes is the closed set the upload ticket endpoint takes.
// TestContractHasNotDrifted pins it to the published contract.
var acceptedUploadTypes = []string{
	"image/jpeg", "image/png", "image/webp", "image/gif",
	"video/mp4", "video/webm", "audio/mpeg", "audio/wav",
	"application/pdf",
	"application/msword",
	"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
	"application/vnd.ms-excel",
	"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
	"application/vnd.ms-powerpoint",
	"application/vnd.openxmlformats-officedocument.presentationml.presentation",
	"application/vnd.apple.keynote",
	"application/vnd.apple.pages",
	"application/vnd.apple.numbers",
	"text/plain",
	"text/markdown",
	ContentTypeLoRA,
}

func isAcceptedUploadType(contentType string) bool {
	for _, accepted := range acceptedUploadTypes {
		if accepted == contentType {
			return true
		}
	}
	return false
}

// CreateUploadURL asks for a presigned slot for exactly one file of exactly
// the declared size. Images are capped at 10 MiB, audio, video and documents
// at 90 MiB (audio and video at 600 seconds), LoRA weights at 2 GiB; the
// ticket's MaxBytes is the authoritative ceiling.
func (c *Client) CreateUploadURL(ctx context.Context, contentType string, size int64) (*UploadTicket, error) {
	if !isAcceptedUploadType(contentType) {
		return nil, fmt.Errorf(
			"popnow: contentType %q is not accepted; use one of %s",
			contentType, strings.Join(acceptedUploadTypes, ", "),
		)
	}
	if size <= 0 {
		return nil, errors.New("popnow: size must be the exact number of bytes you are about to upload")
	}
	out := &UploadTicket{}
	if err := c.do(ctx, request{
		method: http.MethodPost,
		path:   "/common/upload-url",
		body:   map[string]any{"contentType": contentType, "bytes": size},
		// No automatic retry: each call allocates a fresh fileId and ticket, so retrying only
		// leaves behind tickets nobody commits.
		replayable: false,
		out:        out,
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// CommitUpload verifies the bytes against the ticket (size, media type,
// signature), copies them to an immutable private object and returns the
// popnow:// URI to put in task input. Repeating a successful commit is
// idempotent.
//
// Code 40003 here means the stored file does not match what the ticket was
// issued for. Do not retry it: ask for a new ticket and upload again.
func (c *Client) CommitUpload(ctx context.Context, fileID string) (*UploadedFile, error) {
	if err := required("fileID", fileID); err != nil {
		return nil, err
	}
	out := &UploadedFile{}
	path := "/files/" + url.PathEscape(fileID) + "/commit"
	if err := c.do(ctx, request{method: http.MethodPost, path: path, replayable: true, out: out}); err != nil {
		return nil, err
	}
	return out, nil
}

// Upload runs all three steps for bytes already in memory: take a ticket, PUT
// the bytes straight into object storage, then commit. Only the URI on the
// returned file belongs in task input.
//
// Uploading is free; you pay for the task that consumes the file. A public
// HTTPS URL can be passed in the input field directly and skips all of this.
func (c *Client) Upload(ctx context.Context, data []byte, contentType string) (*UploadedFile, error) {
	if len(data) == 0 {
		return nil, errors.New("popnow: refusing to upload an empty file")
	}
	return c.UploadReader(ctx, bytes.NewReader(data), int64(len(data)), contentType)
}

// UploadReader is Upload for a stream of a known size, so a large video or a
// LoRA file never has to sit in memory. r must yield exactly size bytes.
func (c *Client) UploadReader(ctx context.Context, r io.Reader, size int64, contentType string) (*UploadedFile, error) {
	if r == nil {
		return nil, errors.New("popnow: reader is required")
	}
	if size <= 0 {
		return nil, errors.New("popnow: refusing to upload an empty file")
	}
	ticket, err := c.CreateUploadURL(ctx, contentType, size)
	if err != nil {
		return nil, err
	}
	if ticket.MaxBytes > 0 && size > ticket.MaxBytes {
		return nil, fmt.Errorf("popnow: file is %d bytes, above this ticket's ceiling of %d", size, ticket.MaxBytes)
	}
	if err := c.putUpload(ctx, ticket, r, size); err != nil {
		return nil, err
	}
	return c.CommitUpload(ctx, ticket.FileID)
}

// UploadFile streams a local file. An empty contentType is inferred from the
// extension, which only works for the accepted media types (.safetensors
// becomes ContentTypeLoRA).
func (c *Client) UploadFile(ctx context.Context, path, contentType string) (*UploadedFile, error) {
	if contentType == "" {
		inferred, ok := uploadContentTypes[strings.ToLower(filepath.Ext(path))]
		if !ok {
			return nil, fmt.Errorf(
				"popnow: cannot infer a content type from %q; pass one of %s",
				filepath.Ext(path), strings.Join(acceptedUploadTypes, ", "),
			)
		}
		contentType = inferred
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("popnow: opening %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("popnow: reading %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("popnow: %s is not a regular file", path)
	}
	return c.UploadReader(ctx, file, info.Size(), contentType)
}

// putUpload sends the bytes to object storage.
func (c *Client) putUpload(ctx context.Context, ticket *UploadTicket, body io.Reader, size int64) error {
	ctx, cancel := context.WithTimeout(ctx, c.uploadTimeout)
	defer cancel()

	method := ticket.Method
	if method == "" {
		method = http.MethodPut
	}
	// Hide any Close method: the transport closes a ReadCloser body, and the caller owns this
	// reader (UploadFile closes its own file).
	req, err := http.NewRequestWithContext(ctx, method, ticket.UploadURL, struct{ io.Reader }{body})
	if err != nil {
		return fmt.Errorf("popnow: building the upload request: %w", withoutSignedURL(err, ticket.UploadURL))
	}
	// A generic reader would otherwise go out chunked, which presigned PUTs refuse.
	req.ContentLength = size
	req.Header.Set("User-Agent", c.userAgent)

	// Every header from the ticket is forwarded verbatim, except that net/http does not read two
	// of them from Header, so a naive loop would drop them silently.
	for name, value := range ticket.Headers {
		switch http.CanonicalHeaderKey(name) {
		case "Content-Length":
			// net/http sends ContentLength, not this header. A mismatch means the ticket was
			// issued for a different file, and uploading anyway would only hit 40003 at commit.
			declared, parseErr := strconv.ParseInt(value, 10, 64)
			if parseErr == nil && declared != size {
				return fmt.Errorf("popnow: this ticket was issued for %d bytes but the file is %d", declared, size)
			}
		case "Host":
			req.Host = value
		default:
			req.Header.Set(name, value)
		}
	}

	// This hop carries no Authorization: the presigned URL is itself the credential, and an extra
	// auth header would hand the API key to object storage.
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("popnow: uploading to object storage: %w", withoutSignedURL(err, ticket.UploadURL))
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes))
		_ = resp.Body.Close()
	}()
	if resp.StatusCode/100 != 2 {
		return &APIError{
			Status:  resp.StatusCode,
			Message: "presigned upload was rejected by object storage",
		}
	}
	return nil
}

// redactedURLPlaceholder stands in for a presigned URL removed from an error.
const redactedURLPlaceholder = "<presigned URL redacted>"

// redactedError renders a failure without the URL it failed against, while
// keeping the cause reachable for errors.Is and errors.As.
type redactedError struct {
	text  string
	cause error
}

func (e *redactedError) Error() string { return e.text }

// Unwrap exposes the cause from below the *url.Error rather than the
// *url.Error itself, so errors.Is(err, context.DeadlineExceeded) keeps working
// while the signed URL is gone from the chain.
func (e *redactedError) Unwrap() error { return e.cause }

// withoutSignedURL strips a presigned URL out of a transport error.
//
// http.Client.Do reports a transport failure as *url.Error, whose Error()
// prints the URL it failed against. On this path that URL is the upload
// ticket: a write authorization for object storage whose credential and
// signature travel in the query string. net/url never masks the query, so the
// default rendering would hand the grant to whoever reads the application log.
func withoutSignedURL(err error, signed string) error {
	if err == nil {
		return nil
	}

	text, cause := err.Error(), err
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		cause = urlErr.Err
		operation := strings.TrimSpace(urlErr.Op)
		if operation == "" {
			operation = "request"
		}
		text = operation + " to " + redactedURLPlaceholder + " failed"
		if cause != nil {
			text += ": " + cause.Error()
		}
	}

	// A literal sweep as a backstop: the cause may have copied the URL into its own text.
	return &redactedError{text: stripLiteral(text, signed), cause: cause}
}

// stripLiteral removes a secret, and separately its query string, from a
// message: escaping or truncation can defeat a full match, and the signature
// lives in the query.
func stripLiteral(text, secret string) string {
	if secret == "" {
		return text
	}
	text = strings.ReplaceAll(text, secret, redactedURLPlaceholder)
	if at := strings.Index(secret, "?"); at >= 0 && at+1 < len(secret) {
		text = strings.ReplaceAll(text, secret[at+1:], redactedURLPlaceholder)
	}
	return text
}

// LoRAImport is the body of ImportLoRA.
type LoRAImport struct {
	// URL is a Hugging Face repo id (owner/repo, when the repo has exactly one
	// .safetensors file), a Hugging Face repo or file page, or a direct https
	// link to a .safetensors file.
	URL string `json:"url"`
	// Token is an optional Hugging Face access token or Civitai API key, used
	// only for this download and never stored.
	Token string `json:"token,omitempty"`
}

// ImportLoRA has the service download a .safetensors LoRA file once and store
// it as your upload, so weights behind a sign-in can be used without making
// them public. Put the returned URI in loras[].path.
//
// The server downloads the whole file inside this call, so it gets the upload
// timeout rather than the ordinary request budget. It is not retried
// automatically: every successful call stores a new copy.
func (c *Client) ImportLoRA(ctx context.Context, input LoRAImport) (*UploadedFile, error) {
	if err := required("URL", input.URL); err != nil {
		return nil, err
	}
	out := &UploadedFile{}
	if err := c.do(ctx, request{
		method:     http.MethodPost,
		path:       "/files/import",
		body:       input,
		replayable: false,
		out:        out,
		timeout:    c.uploadTimeout,
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateDownloadURL issues a fresh signed link to one output of a finished
// task. A finished task already carries usable links, so this is only needed
// once those have expired; polling the task again does the same job.
//
// Leave key empty to select the first output. Do not attach your API key when
// fetching the link: it is a bearer grant on its own.
func (c *Client) CreateDownloadURL(ctx context.Context, taskID, key string) (*DownloadTicket, error) {
	if err := required("taskID", taskID); err != nil {
		return nil, err
	}
	body := map[string]string{"taskId": taskID}
	if key != "" {
		body["key"] = key
	}
	out := &DownloadTicket{}
	if err := c.do(ctx, request{
		method: http.MethodPost, path: "/common/download-url", body: body, replayable: true, out: out,
	}); err != nil {
		return nil, err
	}
	return out, nil
}
