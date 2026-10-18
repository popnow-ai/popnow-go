// Package popnow is the Go client for the PopNow API.
//
// PopNow is a browser-based AI studio for videos, images and music. Its
// developer API serves the same image, video and audio models, prices and
// credit wallet as https://popnow.ai. This package covers the asynchronous
// media workflow end to end: discover a model and its input schema, upload
// local reference material, quote a request, submit it, wait for a terminal
// state, collect the result, keep it in the asset library, and verify the
// webhook that announces it.
//
// It depends on the standard library only.
//
// Two things decide most of the code in this package:
//
//   - Every /api/v1 response is the {code,msg,data,request_id} envelope, and a
//     successful HTTP status does not mean the call succeeded. Branch on the
//     business code, never on the message text.
//   - HTTP 202 from task creation means accepted, not finished. Success or
//     failure is reported by the task's state, read back with GetTask.
//
// Billing: 1 credit = $0.01. Amounts are reported both as integer credits
// (MaxCredits, EstimatedCredits, Credits, ...) and as the equivalent USD
// decimal string (MaxCharge, EstimatedCost, Cost, ...).
//
// The contract this package is written against is published at
// https://popnow.ai/docs/openapi.yaml.
package popnow

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Version is this package's released version, and the only place that number
// is written down. UserAgent derives from it, so bumping it here is the whole
// release edit.
//
// Keep it equal to the git tag it ships under, without the leading v. A tagged
// module exposes its version only to the programs that depend on it, never to
// itself, so nothing can read it back at run time.
const Version = "0.1.0"

// UserAgent identifies this client on every API request.
//
// It is always sent explicitly. Without it net/http sends Go-http-client/1.1,
// which says nothing beyond "a Go program", and the API edge refuses some
// generic default client agents outright. A versioned agent also makes a bad
// release or a version-specific bug visible in request logs.
const UserAgent = "popnow-go/" + Version + " (+https://github.com/popnow-ai/popnow-go)"

// Defaults for a new Client. Every one of them can be overridden in Options.
const (
	// APIBaseURL is the production API root for the native endpoints.
	APIBaseURL = "https://api.popnow.ai/api/v1"

	// APIKeyEnv is the environment variable New reads when Options.APIKey is
	// empty.
	APIKeyEnv = "POPNOW_API_KEY"

	// DefaultRequestTimeout bounds one JSON API attempt, not the whole call.
	// Give the context its own deadline to bound a call including retries.
	DefaultRequestTimeout = 30 * time.Second

	// DefaultUploadTimeout bounds the PUT that streams bytes into object
	// storage, and the server-side download behind ImportLoRA. It is far
	// longer than DefaultRequestTimeout: audio and video may be 90 MiB and
	// LoRA weights 2 GiB, and a 30 second cut-off would read like a network
	// fault while really being this client hanging up on itself.
	DefaultUploadTimeout = 30 * time.Minute

	// DefaultWaitTimeout bounds WaitForTerminal. It is a local safety bound,
	// not a service level: reaching it says nothing about the remote task.
	DefaultWaitTimeout = 10 * time.Minute

	// DefaultPollInterval is the first gap between task polls; the gap grows
	// by half on every pass and stops at MaxPollInterval.
	DefaultPollInterval = 2 * time.Second
	// MaxPollInterval caps the growth of the polling gap.
	MaxPollInterval = 15 * time.Second

	// DefaultMaxRetries is how many extra attempts a replayable request gets.
	DefaultMaxRetries = 3

	// WebhookTolerance is the default clock skew the webhook verifiers accept.
	WebhookTolerance = 5 * time.Minute

	baseRetryDelay   = 500 * time.Millisecond
	maxRetryDelay    = 8 * time.Second
	maxResponseBytes = 8 << 20
)

// Task states. The two in-flight states are the stable part of this set:
// treat anything else as terminal rather than listing the terminal values, so
// that a state added later ends a wait instead of extending it forever.
// StateCanceled exists only on old records.
const (
	StateQueued    = "queued"
	StateRunning   = "running"
	StateSucceeded = "succeeded"
	StateFailed    = "failed"
	StateCanceled  = "canceled"
	StateExpired   = "expired"
)

// Failure identifiers reported in Task.ErrorCode. The set is closed; handle
// any value you do not recognise the way you handle ErrorCodeUpstreamFailed.
const (
	ErrorCodeInvalidRequest         = "invalid_request"
	ErrorCodeUnsupportedCombination = "unsupported_combination"
	ErrorCodeContentRejected        = "content_rejected"
	ErrorCodeRateLimited            = "rate_limited"
	ErrorCodeUpstreamUnavailable    = "upstream_unavailable"
	ErrorCodeGenerationFailed       = "generation_failed"
	ErrorCodeTimeout                = "timeout"
	ErrorCodeInvalidAsset           = "invalid_asset"
	ErrorCodeUpstreamFailed         = "upstream_failed"
)

// Business codes. They travel in the envelope's code field and are the thing
// to branch on: several of them share one HTTP status.
//
//	40003  the uploaded bytes do not match their ticket (size, media type or
//	       signature). Not retryable: request a fresh ticket with the real
//	       content type and size and upload again.
//	40004  the request is valid but no deployment serves this exact
//	       combination of settings right now. Not retryable: change the
//	       parameter named in the message, or pick another model.
//	40201  not enough credits for this task's hold. Buy credits, then resend.
//	40202  a key spend cap was reached.
//	40301  this key may not call this model.
//	40302  the caller's address is outside this key's IP allow-list.
//	40303  the service is not offered in the caller's region.
//	40310  the account has not verified its email address yet. The same key
//	       works as soon as the address is verified.
//	40901  the quote expired, or the request or price changed since it was
//	       signed. Quote again, then resubmit.
//	503    a supporting dependency is briefly unavailable. Retryable; honour
//	       Retry-After when the response carries it.
//	50301  the model has no usable deployment or effective price right now.
//	50302  a synchronous generation failed and the charge was refunded.
//	       Resend under a new idempotency key: the old key replays the
//	       recorded failure. This client therefore never resends it for you.
//
// 503, 50301 and 50302 share HTTP 503 and mean three different things, which
// is why this client reads the business code first and the status second.
const (
	CodeSuccess                = 200
	CodeBadRequest             = 400
	CodeUploadMismatch         = 40003
	CodeUnsupportedCombination = 40004
	CodeUnauthorized           = 401
	CodeInsufficientCredits    = 40201
	CodeSpendCapReached        = 40202
	CodeForbidden              = 403
	CodeModelNotAllowed        = 40301
	CodeIPNotAllowed           = 40302
	CodeRegionUnavailable      = 40303
	CodeEmailNotVerified       = 40310
	CodeNotFound               = 404
	CodeConflict               = 409
	CodeQuoteRejected          = 40901
	CodePayloadTooLarge        = 413
	CodeRateLimited            = 429
	CodeServerError            = 500
	CodeDependencyUnavailable  = 503
	CodeModelUnavailable       = 50301
	CodeGenerationRefunded     = 50302
)

// retryableHTTP holds the statuses worth another attempt when the response
// body is not the envelope at all, for example an edge network error page.
var retryableHTTP = map[int]bool{408: true, 429: true, 500: true, 502: true, 503: true, 504: true}

// retryableCode holds the business codes worth another attempt with the exact
// same bytes. CodeGenerationRefunded is deliberately absent: it needs a new
// idempotency key, so resending it here would be a different request.
var retryableCode = map[int]bool{
	CodeRateLimited:           true,
	CodeServerError:           true,
	CodeDependencyUnavailable: true,
	CodeModelUnavailable:      true,
}

// APIError is a rejected call: an HTTP status, the business code to branch on,
// and the request ID to quote to support. Recover it with errors.As.
type APIError struct {
	// Status is the HTTP status. It is 0 when no response was parsed.
	Status int
	// Code is the business code from the envelope, 0 when the body was not an
	// envelope. Branch on this, not on Message.
	Code int
	// Message is the server's explanation, in English unless the account or
	// the Accept-Language header selects another language. Never parse it.
	Message string
	// Reason is the machine-readable data.reason some failures carry, for
	// example "asset_library_locked" or "asset_daily_limit". Empty when the
	// failure has none. New values may appear; treat unknown ones as empty.
	Reason string
	// RequestID correlates this call with the server's logs.
	RequestID string
	// RetryAfter is the server's own backoff request, 0 when absent.
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	parts := []string{fmt.Sprintf("popnow: %s", e.Message)}
	if e.Code != 0 {
		parts = append(parts, fmt.Sprintf("code %d", e.Code))
	}
	if e.Reason != "" {
		parts = append(parts, fmt.Sprintf("reason %s", e.Reason))
	}
	if e.Status != 0 {
		parts = append(parts, fmt.Sprintf("HTTP %d", e.Status))
	}
	if e.RequestID != "" {
		parts = append(parts, fmt.Sprintf("request_id %s", e.RequestID))
	}
	return strings.Join(parts, ", ")
}

// Retryable reports whether sending the identical request again is worth it.
func (e *APIError) Retryable() bool {
	if e.Code != 0 {
		return retryableCode[e.Code]
	}
	return retryableHTTP[e.Status]
}

// Refunded reports code 50302: generation failed and the hold was released,
// so this cost nothing. Submit the same input again under a new idempotency
// key.
func (e *APIError) Refunded() bool { return e.Code == CodeGenerationRefunded }

// WaitTimeoutError means a local polling deadline elapsed. It is not evidence
// that the task failed: the generation is still running somewhere, so store
// TaskID and reconcile it later instead of submitting the work twice.
type WaitTimeoutError struct {
	TaskID    string
	Timeout   time.Duration
	LastState string
}

func (e *WaitTimeoutError) Error() string {
	return fmt.Sprintf(
		"popnow: task %s did not finish within the local %s deadline (last state %q); its remote state is unknown",
		e.TaskID, e.Timeout, e.LastState,
	)
}

// Unwrap lets errors.Is(err, context.DeadlineExceeded) recognise this.
func (e *WaitTimeoutError) Unwrap() error { return context.DeadlineExceeded }

// Webhook rejection reasons. Every one of them wraps ErrWebhookRejected, so a
// handler that only wants to answer 401 can test for that alone.
var (
	ErrWebhookRejected  = errors.New("popnow: webhook rejected")
	ErrWebhookStale     = fmt.Errorf("%w: timestamp outside the tolerance window", ErrWebhookRejected)
	ErrWebhookSignature = fmt.Errorf("%w: signature mismatch", ErrWebhookRejected)
)
