package popnow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// CreateTaskInput is the body shared by Quote and CreateTask.
type CreateTaskInput struct {
	// Model is an exact call name from the catalogue, in
	// <publisher>/<model>/<task> form, for example
	// "alibaba/z-image-turbo/text-to-image".
	Model string `json:"model"`
	// Input is validated against that model's input schema. Media fields take
	// a public HTTPS URL, a committed popnow://f/ upload URI, a saved
	// popnow://a/ asset URI, or a small Base64 image data URI.
	Input map[string]any `json:"input"`
	// CallBackURL is an optional public HTTPS endpoint for terminal delivery.
	// Verify every delivery with VerifyWebhook before acting on it.
	CallBackURL string `json:"callBackUrl,omitempty"`
	// QuoteID together with ExpectedCredits (or ExpectedCost) confirms a
	// price: if the price moved since the quote was signed, the task is
	// rejected with code 40901 instead of being accepted at the new amount.
	QuoteID string `json:"quoteId,omitempty"`
	// ExpectedCredits is the confirmed number of credits to hold, as returned
	// in Quote.MaxCredits. It is a pointer because 0 is a real amount. When
	// both are set, ExpectedCredits wins over ExpectedCost.
	ExpectedCredits *int64 `json:"expectedCredits,omitempty"`
	// ExpectedCost is the same confirmation as a USD decimal string.
	ExpectedCost string `json:"expectedCost,omitempty"`
}

// validate is the local check shared by Quote and CreateTask.
func (in CreateTaskInput) validate() error {
	if strings.TrimSpace(in.Model) == "" {
		return errors.New("popnow: input.Model is required")
	}
	if len(in.Input) == 0 {
		return errors.New("popnow: input.Input is required")
	}
	if in.ExpectedCredits != nil && *in.ExpectedCredits < 0 {
		return errors.New("popnow: input.ExpectedCredits must not be negative")
	}
	if in.CallBackURL != "" {
		parsed, err := url.Parse(in.CallBackURL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
			// The server rejects anything else with "Invalid callback URL"; saying so here saves
			// a round trip and names the actual rule.
			return fmt.Errorf("popnow: CallBackURL must be a public https:// URL, got %q", in.CallBackURL)
		}
	}
	return nil
}

// Quote is a signed price for one exact request. It reserves nothing and
// guarantees no capacity; it only fixes the price for five minutes.
type Quote struct {
	// QuoteID goes back to CreateTask together with MaxCredits.
	QuoteID string `json:"quoteId"`
	// Model is the resolved endpoint identifier.
	Model string `json:"model"`
	// MaxCredits is the most this request can cost, in credits. It is held
	// when the task is accepted. Pass it back as ExpectedCredits.
	MaxCredits int64 `json:"maxCredits"`
	// ExpectedCredits is what the request usually costs. It equals MaxCredits
	// unless the final charge depends on the output, and the charge never
	// exceeds MaxCredits.
	ExpectedCredits int64 `json:"expectedCredits"`
	// EstimatedCost and MaxCharge are the same amounts as USD decimal strings.
	EstimatedCost string `json:"estimatedCost"`
	MaxCharge     string `json:"maxCharge"`
	Currency      string `json:"currency"`
	// Quantity and Unit explain how the amount was reached.
	Quantity  string    `json:"quantity"`
	Unit      string    `json:"unit"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// Quote prices one exact request for five minutes without creating a task,
// holding credits or starting anything. It runs the same validation and
// admission rules as CreateTask, so it is also the cheapest way to find out
// that an input is wrong.
//
// Pass the returned QuoteID and MaxCredits to CreateTask to refuse a price
// that moved in the meantime.
func (c *Client) Quote(ctx context.Context, input CreateTaskInput) (*Quote, error) {
	if err := input.validate(); err != nil {
		return nil, err
	}
	// Send only the fields that are priced: passing a previous quoteId and expected amount back
	// would be asking "quote me at this old price", while a quote exists to discover that the
	// price moved.
	body := CreateTaskInput{Model: input.Model, Input: input.Input, CallBackURL: input.CallBackURL}
	out := &Quote{}
	// A quote creates no task and holds no credits, so retrying is safe.
	if err := c.do(ctx, request{method: http.MethodPost, path: "/jobs/quote", body: body, replayable: true, out: out}); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateTaskOptions carries the optional header and query that accompany a
// submission.
type CreateTaskOptions struct {
	// RetentionSeconds, when non-nil, shortens how long this task's outputs
	// and its stored request text are kept (sent as X-PopNow-Retention). It
	// can only shorten: the effective value is the smallest of this, the
	// account setting and the platform maximum, and it is reported back in
	// Task.Retention.
	//
	// It is a pointer because 0 is a meaningful value - the outputs go as
	// soon as the task reaches a terminal state - so it must not be what an
	// empty options struct sends.
	RetentionSeconds *int

	// WaitSeconds, when non-nil, asks the service to hold the connection open
	// for that many seconds waiting for the task to finish, so the common case
	// needs no polling loop. Finishing inside the budget fills
	// CreateTaskResult.Task with the complete record; running out of it
	// returns the ordinary acceptance and you poll from there.
	//
	// It is an optimisation, not a guarantee. The value is sent exactly as
	// given: the service clamps anything above its ceiling (60 today) and
	// ignores, rather than rejects, a value that is not a positive integer.
	// No ceiling is enforced here, because a copy of the platform's number is
	// a constant that can go stale without anything saying so.
	//
	// Setting it also lifts this one request's own deadline to cover the wait.
	WaitSeconds *int
}

// CreateTaskResult is the acceptance record, not a result.
type CreateTaskResult struct {
	TaskID string `json:"taskId"`
	// State is queued for a new submission. An idempotent replay reports the
	// original task's current state instead, which may already be terminal.
	State string `json:"state"`
	// EstimatedCredits is the number of credits held, and the ceiling on the
	// final charge. EstimatedCost is the same amount in USD. Unused credits
	// are released when the task settles.
	//
	// Both are zero when Task is non-nil: a task that already finished has a
	// real cost instead of an estimate, in Task.Credits and Task.Cost.
	EstimatedCredits int64  `json:"estimatedCredits"`
	EstimatedCost    string `json:"estimatedCost"`
	// DeadlineAt is the server's execution deadline, fixed at acceptance. It
	// is not a local wait bound and not an artefact expiry.
	DeadlineAt time.Time `json:"deadlineAt"`

	// Task is the complete terminal record, non-nil only when
	// CreateTaskOptions.WaitSeconds was set and the task finished inside that
	// budget. It is exactly what GetTask would return.
	//
	// Nil is the ordinary outcome and is not a failure: the wait ran out, the
	// task is still running, and you poll from here. Code that reads Task
	// without checking for nil works right up until the first slow generation.
	Task *Task `json:"-"`
}

// InFlight reports whether the task was still queued or running when the
// service produced this answer. Everything else is terminal, including a state
// this client has never heard of.
func (r *CreateTaskResult) InFlight() bool {
	return r.State == StateQueued || r.State == StateRunning
}

// RetryTaskResult additionally names the task this one was created from.
type RetryTaskResult struct {
	CreateTaskResult
	SourceTaskID string `json:"sourceTaskId"`
}

// CreateTask submits a generation and returns as soon as it is accepted.
//
// Acceptance is not completion. The returned state is queued; read the outcome
// back with GetTask or WaitForTerminal. EstimatedCredits is held, not charged,
// and it is also the ceiling: the settled charge can only be lower.
//
// idempotencyKey is required by this client even though the contract allows
// it to be omitted, because the automatic resend on a network fault is only
// sound when it exists. Without a key, a request that times out leaves you
// unable to tell an accepted task from a lost one, and the obvious reaction -
// send it again - is a second generation and a second charge. Reuse the same
// value for every attempt at the same logical submission: for 24 hours, and
// for this API key, a repeat returns the original task instead of creating
// another.
func (c *Client) CreateTask(
	ctx context.Context,
	input CreateTaskInput,
	idempotencyKey string,
	opts CreateTaskOptions,
) (*CreateTaskResult, error) {
	if err := input.validate(); err != nil {
		return nil, err
	}
	key, err := checkIdempotencyKey(idempotencyKey)
	if err != nil {
		return nil, err
	}

	headers := map[string]string{"Idempotency-Key": key}
	if opts.RetentionSeconds != nil {
		// Reject negatives only. The upper bound is the platform's number; anything over it is
		// clamped server-side and the effective value is read back from Task.Retention.
		if *opts.RetentionSeconds < 0 {
			return nil, errors.New("popnow: RetentionSeconds must not be negative")
		}
		headers["X-PopNow-Retention"] = strconv.Itoa(*opts.RetentionSeconds)
	}

	path := "/jobs/createTask"
	// A synchronous wait gets a budget of its own. Without this, any wait longer than
	// requestTimeout would time out in this client's hands while the response was on its way,
	// then be retried, and end in a "context deadline exceeded" that points at the network while
	// the task keeps running and keeps being billed.
	timeout := time.Duration(0)
	if opts.WaitSeconds != nil {
		if *opts.WaitSeconds < 0 {
			return nil, errors.New("popnow: WaitSeconds must not be negative")
		}
		path += "?" + url.Values{"wait": []string{strconv.Itoa(*opts.WaitSeconds)}}.Encode()
		// Add one ordinary budget for the round trip itself, or a server that uses its full wait
		// leaves this client sitting exactly on the boundary.
		timeout = time.Duration(*opts.WaitSeconds)*time.Second + c.requestTimeout
	}

	// The two statuses return two different shapes (200 a complete task record, 202 an acceptance
	// receipt), so take the raw bytes first and decide how to decode once the status is known.
	var data json.RawMessage
	var status int
	if err := c.do(ctx, request{
		method:  http.MethodPost,
		path:    path,
		body:    input,
		headers: headers,
		// Automatic retry is only safe under an idempotency key: the server folds a repeated key
		// back onto the original task.
		replayable: true,
		out:        &data,
		timeout:    timeout,
		status:     &status,
	}); err != nil {
		return nil, err
	}

	out := &CreateTaskResult{}
	if err := json.Unmarshal(data, out); err != nil {
		return nil, fmt.Errorf("popnow: decoding the %s response: %w", path, err)
	}
	if status == http.StatusOK {
		// A 200 only appears when the wait reached a terminal state, and its body is the same task
		// record recordInfo returns. Terminal does not mean successful; read Task.State.
		task := &Task{}
		if err := json.Unmarshal(data, task); err != nil {
			return nil, fmt.Errorf("popnow: decoding the %s task record: %w", path, err)
		}
		// Never attach a still-running record to Task: callers use exactly that field to decide
		// whether to keep polling. Leaving it nil costs one extra poll.
		if !task.InFlight() {
			out.Task = task
		}
	}
	return out, nil
}

// OutputAsset is one generated artefact of a task.
type OutputAsset struct {
	// Key identifies this output for CreateDownloadURL and CreateAsset.
	Key string `json:"key,omitempty"`
	// URL is a signed link that works on its own. Never attach your API key to
	// it: it is already a bearer grant. It is absent while Pending or
	// Unavailable is true.
	URL string `json:"url,omitempty"`
	// ExpiresAt is this link's expiry, normally about twenty minutes out. It
	// is not the retention deadline of the result; poll again for a fresh link.
	ExpiresAt       *time.Time `json:"expiresAt,omitempty"`
	MIME            string     `json:"mime,omitempty"`
	Width           int        `json:"width,omitempty"`
	Height          int        `json:"height,omitempty"`
	DurationSeconds float64    `json:"durationSeconds,omitempty"`
	Bytes           int64      `json:"bytes,omitempty"`
	// Role is video, image, audio or a model-specific role such as segment.
	Role string `json:"role,omitempty"`
	// Pending means the artefact is still being copied into storage.
	Pending bool `json:"pending,omitempty"`
	// Unavailable means it will not arrive; do not keep waiting for it.
	Unavailable bool `json:"unavailable,omitempty"`
	// Deleted means the owner permanently deleted this one output. It always
	// comes with Unavailable and no Key or URL.
	Deleted bool `json:"deleted,omitempty"`
	// Layer is set only on the outputs of a layer-decomposition task.
	Layer *AssetLayer `json:"layer,omitempty"`
}

// AssetLayer places one output of a layer-decomposition task.
//
// The first asset is the base image (ZIndex 0) and every other asset is one
// transparent PNG layer. Stack them by ascending ZIndex to rebuild the
// picture, rather than relying on the order of Assets.
type AssetLayer struct {
	ZIndex int `json:"zIndex"`
	// Name is the model's own label for the layer, when it gives one.
	Name string `json:"name,omitempty"`
	// BoundingBox is [x1, y1, x2, y2] in base-image pixels. It is nil on the
	// base image and on models that do not report positions.
	BoundingBox []int `json:"boundingBox,omitempty"`
}

// TaskOutput is the result payload of a finished task.
//
// Not every endpoint produces files. Transcription answers in Text and returns
// no assets, so check what you actually got before reaching for Assets[0].
type TaskOutput struct {
	// Text is the plain-text result. For a multichannel transcription it holds
	// one line per channel, in channel order.
	Text string `json:"text,omitempty"`
	// Transcript is the structured speech-to-text result. Nil on every other
	// task.
	Transcript *TaskTranscript `json:"transcript,omitempty"`
	Assets     []OutputAsset   `json:"assets,omitempty"`
}

// TaskTranscript is a structured transcription.
type TaskTranscript struct {
	Text string `json:"text,omitempty"`
	// Language is the detected language as the model reports it ("en");
	// LanguageCode is the ISO 639-3 form ("eng") when known.
	Language     string `json:"language,omitempty"`
	LanguageCode string `json:"languageCode,omitempty"`
	// LanguageProbability is a pointer because "not reported" and "zero
	// confidence" are different.
	LanguageProbability *float64                `json:"languageProbability,omitempty"`
	DurationSeconds     float64                 `json:"durationSeconds,omitempty"`
	Words               []TaskTranscriptWord    `json:"words,omitempty"`
	Channels            []TaskTranscriptChannel `json:"channels,omitempty"`
}

// TaskTranscriptWord is one timed segment of a transcript, in seconds.
type TaskTranscriptWord struct {
	Text      string  `json:"text"`
	Start     float64 `json:"start"`
	End       float64 `json:"end"`
	Type      string  `json:"type,omitempty"`
	SpeakerID string  `json:"speakerId,omitempty"`
}

// TaskTranscriptChannel is the transcript of one audio channel.
type TaskTranscriptChannel struct {
	Index        int                  `json:"index"`
	Text         string               `json:"text,omitempty"`
	Language     string               `json:"language,omitempty"`
	LanguageCode string               `json:"languageCode,omitempty"`
	Words        []TaskTranscriptWord `json:"words,omitempty"`
}

// TaskRetention says when this task's content disappears and which layer
// decided it: header, account or platform.
type TaskRetention struct {
	OutputsExpireAt time.Time `json:"outputsExpireAt"`
	PromptsExpireAt time.Time `json:"promptsExpireAt"`
	Source          string    `json:"source"`
}

// Task is the full record returned by GetTask. Terminal and non-terminal
// states share this shape.
type Task struct {
	TaskID string `json:"taskId"`
	// SourceTaskID is set when this task came from RetryTask.
	SourceTaskID string `json:"sourceTaskId,omitempty"`
	Model        string `json:"model"`
	State        string `json:"state"`
	// Input is the normalized input, including the random seed the service
	// picked when you omitted one. Omitted once retention has redacted it.
	Input  map[string]any `json:"input,omitempty"`
	Output *TaskOutput    `json:"output,omitempty"`
	// ErrorCode is one of the nine ErrorCode constants. Branch on it.
	ErrorCode string `json:"errorCode,omitempty"`
	// ErrorMessage explains the failure. Do not parse it.
	ErrorMessage string `json:"errorMessage,omitempty"`
	// Credits and Cost are the same amount: the final charge once Settled is
	// true, and the held estimate before that. A failed or expired task
	// normally releases its hold in full; read them once Settled is true
	// instead of assuming.
	Credits int64  `json:"credits"`
	Cost    string `json:"cost"`
	Settled bool   `json:"settled"`

	CreatedAt        time.Time      `json:"createdAt"`
	DeadlineAt       *time.Time     `json:"deadlineAt,omitempty"`
	CompletedAt      *time.Time     `json:"completedAt,omitempty"`
	ContentState     string         `json:"contentState,omitempty"`
	ContentRemovedBy string         `json:"contentRemovedBy,omitempty"`
	PurgedAt         *time.Time     `json:"purgedAt,omitempty"`
	Retention        *TaskRetention `json:"retention,omitempty"`
}

// InFlight reports whether the task is still queued or running. Everything
// else is terminal, including a state this client has never heard of.
func (t *Task) InFlight() bool {
	return t.State == StateQueued || t.State == StateRunning
}

// ReadyAssets returns the artefacts that have a usable URL right now.
func (t *Task) ReadyAssets() []OutputAsset {
	if t == nil || t.Output == nil {
		return nil
	}
	ready := make([]OutputAsset, 0, len(t.Output.Assets))
	for _, asset := range t.Output.Assets {
		if asset.URL != "" && !asset.Unavailable {
			ready = append(ready, asset)
		}
	}
	return ready
}

// HasPendingAssets reports whether an artefact is still being copied. A task
// can be succeeded with its files not yet linkable.
func (t *Task) HasPendingAssets() bool {
	if t == nil || t.Output == nil {
		return false
	}
	for _, asset := range t.Output.Assets {
		if asset.Pending && !asset.Unavailable {
			return true
		}
	}
	return false
}

// GetTask reads one task created by this API key. An unknown task, another
// account's task and a sibling key's task are all a 404; the three cases are
// deliberately indistinguishable.
func (c *Client) GetTask(ctx context.Context, taskID string) (*Task, error) {
	if err := required("taskID", taskID); err != nil {
		return nil, err
	}
	out := &Task{}
	path := "/jobs/recordInfo?" + url.Values{"taskId": []string{taskID}}.Encode()
	if err := c.do(ctx, request{method: http.MethodGet, path: path, replayable: true, out: out}); err != nil {
		return nil, err
	}
	return out, nil
}

// WaitOptions tunes WaitForTerminal. Every zero field takes its default.
type WaitOptions struct {
	// Timeout is the local bound on the whole wait, DefaultWaitTimeout by
	// default. It is not a service level.
	Timeout time.Duration
	// Interval is the first gap between polls, DefaultPollInterval by default.
	Interval time.Duration
	// MaxInterval caps how far that gap grows, MaxPollInterval by default.
	MaxInterval time.Duration
	// AcceptPendingAssets returns a terminal task even when its artefacts are
	// still being copied, so they have no URL yet. Leave it false to get back
	// a record you can actually download from.
	AcceptPendingAssets bool
}

// WaitForTerminal polls a task until it stops being queued or running, backing
// off between reads.
//
// Terminal is defined as "neither of the two in-flight states" rather than as
// a list of the known endings, so a state added later ends the wait instead of
// extending it forever. Only "succeeded" carries a result.
//
// A local timeout is not a verdict: it returns *WaitTimeoutError, and the
// generation is still running. Keep the task ID and reconcile it later, or use
// a callback URL instead of waiting.
func (c *Client) WaitForTerminal(ctx context.Context, taskID string, opts WaitOptions) (*Task, error) {
	if err := required("taskID", taskID); err != nil {
		return nil, err
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = c.waitTimeout
	}
	interval := opts.Interval
	if interval <= 0 {
		interval = DefaultPollInterval
	}
	maxInterval := opts.MaxInterval
	if maxInterval <= 0 {
		maxInterval = MaxPollInterval
	}

	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	lastState := ""
	for {
		task, err := c.GetTask(waitCtx, taskID)
		if err != nil {
			if fatal := c.waitError(ctx, waitCtx, taskID, timeout, lastState, err); fatal != nil {
				return nil, fatal
			}
			// This attempt timed out, but budget remains: keep polling.
		} else {
			lastState = task.State
			if !task.InFlight() && (opts.AcceptPendingAssets || !task.HasPendingAssets()) {
				return task, nil
			}
		}
		if err := c.sleep(waitCtx, c.jitter(interval)); err != nil {
			if fatal := c.waitError(ctx, waitCtx, taskID, timeout, lastState, err); fatal != nil {
				return nil, fatal
			}
			return nil, &WaitTimeoutError{TaskID: taskID, Timeout: timeout, LastState: lastState}
		}
		if interval = interval * 3 / 2; interval > maxInterval {
			interval = maxInterval
		}
	}
}

// waitError classifies a failed poll. It returns the error to give up with, or
// nil when the wait still has budget and should simply poll again.
//
//   - the caller cancelled: hand their own error back unchanged.
//   - the local wait budget ran out: *WaitTimeoutError, which carries the task
//     ID the caller needs to reconcile a generation that is still running.
//   - one poll timed out while the budget is still alive: not a verdict, so
//     poll again. Answers from the server (404, 401, ...) are never swallowed.
func (c *Client) waitError(
	ctx context.Context,
	waitCtx context.Context,
	taskID string,
	timeout time.Duration,
	lastState string,
	err error,
) error {
	switch {
	case ctx.Err() != nil:
		return err
	case errors.Is(waitCtx.Err(), context.DeadlineExceeded):
		return &WaitTimeoutError{TaskID: taskID, Timeout: timeout, LastState: lastState}
	case errors.Is(err, context.DeadlineExceeded):
		return nil
	default:
		return err
	}
}

// RetryTask creates a new task from a failed or expired one. The source task
// is never modified, and the current schema, price, permissions and balance
// are all evaluated again, so a retry can cost a different amount or be
// refused.
//
// The source must be terminal and settled; retrying too early returns 409, and
// retrying a succeeded task returns 404. Retention is inherited from the
// source. The idempotency key is scoped to the source task.
func (c *Client) RetryTask(ctx context.Context, taskID, idempotencyKey string) (*RetryTaskResult, error) {
	if err := required("taskID", taskID); err != nil {
		return nil, err
	}
	key, err := checkIdempotencyKey(idempotencyKey)
	if err != nil {
		return nil, err
	}
	out := &RetryTaskResult{}
	if err := c.do(ctx, request{
		method:     http.MethodPost,
		path:       "/jobs/retry",
		body:       map[string]string{"taskId": taskID},
		headers:    map[string]string{"Idempotency-Key": key},
		replayable: true,
		out:        out,
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// PurgeResult reports what PurgeTask destroyed.
type PurgeResult struct {
	TaskID string `json:"taskId"`
	// ContentState is purged after a successful destroy and on every repeat.
	// Still present means nothing was removed and the call may be retried.
	ContentState string `json:"contentState"`
	// PurgedAt is the time of the first destroy, not of this call.
	PurgedAt         *time.Time `json:"purgedAt,omitempty"`
	ContentRemovedBy string     `json:"contentRemovedBy,omitempty"`
	// BillingRetained is always true: destroying content never touches the
	// ledger or the charged amount.
	BillingRetained bool `json:"billingRetained"`
	// MediaDeletionPending is true while the background sweep is still
	// removing the stored objects, which takes about a minute.
	MediaDeletionPending bool `json:"mediaDeletionPending"`
}

// PurgeTask destroys a finished task's stored content: the generated objects,
// the result payload and the request text including the prompt. The spend
// record stays.
//
// The task ID is the idempotency key, so a repeat after a timeout is safe and
// returns the original PurgedAt. A queued or running task returns 400.
func (c *Client) PurgeTask(ctx context.Context, taskID string) (*PurgeResult, error) {
	if err := required("taskID", taskID); err != nil {
		return nil, err
	}
	out := &PurgeResult{}
	if err := c.do(ctx, request{
		method:     http.MethodPost,
		path:       "/jobs/purge",
		body:       map[string]string{"taskId": taskID},
		replayable: true,
		out:        out,
	}); err != nil {
		return nil, err
	}
	return out, nil
}

// TaskListItem is one row of task history: metadata only, with no input,
// output or signed media URL. Read a selected task with GetTask.
type TaskListItem struct {
	TaskID string `json:"taskId"`
	// RequestID is the X-Request-Id of the request that created the task.
	RequestID string `json:"requestId,omitempty"`
	Model     string `json:"model"`
	State     string `json:"state"`
	// Credits and Cost are the final charge once Settled is true, and the
	// held estimate before that.
	Credits    int64     `json:"credits"`
	Cost       string    `json:"cost"`
	Settled    bool      `json:"settled"`
	CreatedAt  time.Time `json:"createdAt"`
	DeadlineAt time.Time `json:"deadlineAt"`
	// CompletedAt is nil while the task is still in flight.
	CompletedAt *time.Time `json:"completedAt,omitempty"`
}

// TaskList is one page of task history.
type TaskList struct {
	Items   []TaskListItem `json:"items"`
	HasMore bool           `json:"hasMore"`
	// NextCursor is present only when HasMore is true. It is opaque.
	NextCursor string `json:"nextCursor,omitempty"`
}

// ListTasksOptions filters task history. A zero field means "do not filter".
type ListTasksOptions struct {
	// From and To are UTC dates in YYYY-MM-DD form and bound a half-open
	// interval [From, To) of at most 92 days. Left empty, the server defaults
	// To to tomorrow in UTC and From to seven days before it.
	From string
	To   string
	// State is one of the six task states.
	State string
	// Model is an exact catalogue identifier or a declared alias.
	Model string
	// Limit is the page size, 1 to 100, 20 by default on the server. The upper
	// bound is deliberately not enforced here.
	Limit int
	// Cursor is the NextCursor of the previous page, passed through unchanged.
	Cursor string
}

// ListTasks returns one page of this API key's task history, newest first.
//
// The scope is this API key alone: tasks of sibling keys and generations
// started on the website are not here. Pages read live state, not a frozen
// snapshot.
func (c *Client) ListTasks(ctx context.Context, opts ListTasksOptions) (*TaskList, error) {
	if opts.Limit < 0 {
		return nil, errors.New("popnow: Limit must not be negative")
	}
	query := url.Values{}
	setIfNotEmpty(query, "from", opts.From)
	setIfNotEmpty(query, "to", opts.To)
	setIfNotEmpty(query, "state", opts.State)
	setIfNotEmpty(query, "model", opts.Model)
	setIfNotEmpty(query, "cursor", opts.Cursor)
	// 0 is Go's zero value, meaning "unset" rather than "give me none".
	if opts.Limit > 0 {
		query.Set("limit", strconv.Itoa(opts.Limit))
	}

	out := &TaskList{}
	if err := c.do(ctx, request{method: http.MethodGet, path: withQuery("/jobs", query), replayable: true, out: out}); err != nil {
		return nil, err
	}
	return out, nil
}

// EachTask walks every page of ListTasks and calls fn once per task. Return
// false from fn to stop early; the error is the first page that failed.
//
// Every filter is repeated unchanged on each request and only the cursor
// moves: a cursor is only meaningful against the query it was issued for.
//
// Pin From and To yourself for any walk that might cross midnight UTC. With
// them empty the server recomputes its default window on every page, so a walk
// that starts at 23:59 and continues at 00:01 splices two different answers
// together and nothing reports it.
func (c *Client) EachTask(ctx context.Context, opts ListTasksOptions, fn func(TaskListItem) bool) error {
	if fn == nil {
		return errors.New("popnow: fn is required")
	}
	for {
		page, err := c.ListTasks(ctx, opts)
		if err != nil {
			return err
		}
		for _, item := range page.Items {
			if !fn(item) {
				return nil
			}
		}
		// Either signal saying "the end" means the end; trusting just one leaves the other free
		// to page forever.
		if !page.HasMore || page.NextCursor == "" {
			return nil
		}
		opts.Cursor = page.NextCursor
	}
}

// Tasks is EachTask as a range-over-func iterator:
//
//	for task, err := range client.Tasks(ctx, popnow.ListTasksOptions{}) {
//		if err != nil {
//			return err
//		}
//		log.Println(task.TaskID)
//	}
//
// A failed page yields one final (zero, err) pair and ends the sequence.
func (c *Client) Tasks(ctx context.Context, opts ListTasksOptions) iter.Seq2[TaskListItem, error] {
	return func(yield func(TaskListItem, error) bool) {
		stopped := false
		err := c.EachTask(ctx, opts, func(item TaskListItem) bool {
			if !yield(item, nil) {
				stopped = true
				return false
			}
			return true
		})
		if err != nil && !stopped {
			yield(TaskListItem{}, err)
		}
	}
}
