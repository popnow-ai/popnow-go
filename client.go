package popnow

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// HTTPDoer is the slice of *http.Client this package needs, so that tests can
// substitute a transport without a network.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Options configures New. Every zero value falls back to a documented default.
type Options struct {
	// APIKey defaults to the POPNOW_API_KEY environment variable. Keep it
	// server-side; it is a bearer credential for your credit wallet.
	APIKey string
	// BaseURL defaults to APIBaseURL.
	BaseURL string
	// HTTPClient defaults to a client with no timeout of its own, because
	// every request here already carries a context deadline.
	HTTPClient HTTPDoer
	// UserAgent replaces the default UserAgent. Anyone embedding this client
	// in their own tool can name that tool here; keep it non-empty and
	// specific, because generic default agents are refused at the edge.
	UserAgent string
	// AcceptLanguage, when set, is sent as Accept-Language to choose the
	// language of error messages (for example "de" or "ja"). Codes never
	// change with the language.
	AcceptLanguage string

	RequestTimeout time.Duration
	UploadTimeout  time.Duration
	WaitTimeout    time.Duration
	// MaxRetries is the number of extra attempts a replayable request gets.
	// Set it to -1 to disable retries entirely.
	MaxRetries int

	// Now and Rand exist for deterministic tests.
	Now  func() time.Time
	Rand func() float64
	// Sleep must return the context error if the context ends first.
	Sleep func(ctx context.Context, d time.Duration) error
}

// Client calls the PopNow API. It is safe for concurrent use.
type Client struct {
	apiKey         string
	baseURL        string
	httpClient     HTTPDoer
	userAgent      string
	acceptLanguage string
	requestTimeout time.Duration
	uploadTimeout  time.Duration
	waitTimeout    time.Duration
	maxRetries     int
	now            func() time.Time
	rand           func() float64
	sleep          func(ctx context.Context, d time.Duration) error
}

// New builds a client. It fails when no API key is available, because every
// endpoint needs one and discovering that at the first call is worse.
func New(opts Options) (*Client, error) {
	key := strings.TrimSpace(opts.APIKey)
	if key == "" {
		key = strings.TrimSpace(os.Getenv(APIKeyEnv))
	}
	if key == "" {
		return nil, errors.New("popnow: an API key is required; set " + APIKeyEnv + " or Options.APIKey")
	}

	base := opts.BaseURL
	if base == "" {
		base = APIBaseURL
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, fmt.Errorf("popnow: BaseURL must be an absolute URL, got %q", base)
	}
	// The API key is a bearer credential, and http hands it over in the clear. Loopback is the
	// exception: that is local development and never touches a network.
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopback(parsed.Hostname())) {
		return nil, fmt.Errorf("popnow: BaseURL must use HTTPS; HTTP is allowed only for loopback, got %q", base)
	}

	client := &Client{
		apiKey:         key,
		baseURL:        strings.TrimRight(base, "/"),
		httpClient:     opts.HTTPClient,
		userAgent:      strings.TrimSpace(opts.UserAgent),
		acceptLanguage: strings.TrimSpace(opts.AcceptLanguage),
		requestTimeout: opts.RequestTimeout,
		uploadTimeout:  opts.UploadTimeout,
		waitTimeout:    opts.WaitTimeout,
		maxRetries:     opts.MaxRetries,
		now:            opts.Now,
		rand:           opts.Rand,
		sleep:          opts.Sleep,
	}
	if client.httpClient == nil {
		client.httpClient = &http.Client{}
	}
	if client.userAgent == "" {
		client.userAgent = UserAgent
	}
	if client.requestTimeout <= 0 {
		client.requestTimeout = DefaultRequestTimeout
	}
	if client.uploadTimeout <= 0 {
		client.uploadTimeout = DefaultUploadTimeout
	}
	if client.waitTimeout <= 0 {
		client.waitTimeout = DefaultWaitTimeout
	}
	if client.maxRetries == 0 {
		client.maxRetries = DefaultMaxRetries
	}
	if client.maxRetries < 0 {
		client.maxRetries = 0
	}
	if client.now == nil {
		client.now = time.Now
	}
	if client.rand == nil {
		// Used only to jitter back-off, never for anything security related.
		client.rand = rand.Float64
	}
	if client.sleep == nil {
		client.sleep = sleepWithContext
	}
	return client, nil
}

// NewIdempotencyKey mints a key with enough entropy that two submissions never
// collide. Store it next to your own record of the job before you send it: the
// point of the key is to be available again after the crash that lost the
// response. A key derived from your own data ("order-8814-render-1") works
// just as well and can be recomputed instead of stored.
func NewIdempotencyKey() (string, error) {
	buf := make([]byte, 16)
	if _, err := cryptorand.Read(buf); err != nil {
		return "", fmt.Errorf("popnow: generating an idempotency key: %w", err)
	}
	return "popnow-" + hex.EncodeToString(buf), nil
}

// maxIdempotencyKeyLength is the contract's own bound on Idempotency-Key.
const maxIdempotencyKeyLength = 128

// checkIdempotencyKey validates a key before it reaches the network.
func checkIdempotencyKey(key string) (string, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return "", errors.New("popnow: an Idempotency-Key is required; see NewIdempotencyKey")
	}
	if len(key) > maxIdempotencyKeyLength {
		return "", fmt.Errorf("popnow: an Idempotency-Key is at most %d characters, got %d", maxIdempotencyKeyLength, len(key))
	}
	return key, nil
}

// envelope is the shape every /api/v1 endpoint returns. Data stays raw so each
// call site decodes it into whatever it actually expects.
type envelope struct {
	Code      int             `json:"code"`
	Msg       string          `json:"msg"`
	Data      json.RawMessage `json:"data"`
	RequestID string          `json:"request_id"`
}

// request describes one call to the API surface.
type request struct {
	method  string
	path    string
	body    any
	headers map[string]string
	// replayable says whether sending these exact bytes again is safe. It
	// gates every automatic retry: a transport error does not prove the server
	// never saw the request, so anything that would create or charge twice is
	// only resent when an idempotency key makes the duplicate harmless.
	replayable bool
	out        any
	// timeout overrides the client's per-attempt deadline for this one call.
	// Zero keeps the default. It exists for requests the service is expected
	// to hold open on purpose, where the ordinary budget would have this
	// client hang up on a response that was going to arrive.
	timeout time.Duration
	// status, when set, receives the HTTP status of the successful response.
	// createTask answers with two different bodies under two different
	// statuses, and the status is the discriminator the contract defines.
	status *int
}

// do performs a request, decodes the envelope and retries what is worth
// retrying.
func (c *Client) do(ctx context.Context, spec request) error {
	for attempt := 0; ; attempt++ {
		status, header, body, err := c.attempt(ctx, spec)
		if err != nil {
			if ctx.Err() != nil || !spec.replayable || attempt >= c.maxRetries {
				return fmt.Errorf("popnow: %s %s: %w", spec.method, spec.path, err)
			}
			if waitErr := c.sleep(ctx, c.backoff(attempt, 0)); waitErr != nil {
				return waitErr
			}
			continue
		}

		retryAfter := parseRetryAfter(header.Get("Retry-After"), c.now())
		var env envelope
		// code is always non-zero, which makes it double as the test for "is this the envelope".
		parsed := json.Unmarshal(body, &env) == nil && env.Code != 0

		if !parsed {
			// Not an envelope - usually an HTML error page from an edge network. Only the HTTP
			// status is available to go on, because there is no business code at all.
			if spec.replayable && attempt < c.maxRetries && retryableHTTP[status] {
				if waitErr := c.sleep(ctx, c.backoff(attempt, retryAfter)); waitErr != nil {
					return waitErr
				}
				continue
			}
			return &APIError{
				Status:     status,
				Message:    fmt.Sprintf("response was not the PopNow envelope: %s", snippet(body)),
				RetryAfter: retryAfter,
			}
		}

		if status/100 != 2 || env.Code != CodeSuccess {
			if spec.replayable && attempt < c.maxRetries && retryableCode[env.Code] {
				if waitErr := c.sleep(ctx, c.backoff(attempt, retryAfter)); waitErr != nil {
					return waitErr
				}
				continue
			}
			return &APIError{
				Status:     status,
				Code:       env.Code,
				Message:    env.Msg,
				Reason:     errorReason(env.Data),
				RequestID:  env.RequestID,
				RetryAfter: retryAfter,
			}
		}

		if spec.status != nil {
			*spec.status = status
		}
		if spec.out == nil {
			return nil
		}
		if len(env.Data) == 0 || string(env.Data) == "null" {
			return &APIError{
				Status:    status,
				Code:      env.Code,
				Message:   "successful envelope carried no data",
				RequestID: env.RequestID,
			}
		}
		if err := json.Unmarshal(env.Data, spec.out); err != nil {
			return fmt.Errorf("popnow: decoding the %s response: %w", spec.path, err)
		}
		return nil
	}
}

// errorReason reads data.reason out of an error envelope, when there is one.
func errorReason(data json.RawMessage) string {
	if len(data) == 0 {
		return ""
	}
	var shape struct {
		Reason string `json:"reason"`
	}
	if json.Unmarshal(data, &shape) != nil {
		return ""
	}
	return shape.Reason
}

// attempt performs exactly one HTTP round trip under its own deadline.
func (c *Client) attempt(ctx context.Context, spec request) (int, http.Header, []byte, error) {
	budget := c.requestTimeout
	if spec.timeout > 0 {
		budget = spec.timeout
	}
	ctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	var payload io.Reader
	if spec.body != nil {
		raw, err := json.Marshal(spec.body)
		if err != nil {
			return 0, nil, nil, fmt.Errorf("encoding the request body: %w", err)
		}
		payload = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, spec.method, c.baseURL+spec.path, payload)
	if err != nil {
		return 0, nil, nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("User-Agent", c.userAgent)
	if c.acceptLanguage != "" {
		req.Header.Set("Accept-Language", c.acceptLanguage)
	}
	if spec.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for name, value := range spec.headers {
		req.Header.Set(name, value)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return 0, nil, nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	// Read ceiling: a broken intermediary can emit bytes indefinitely, and without a ceiling that
	// consumes the whole process.
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return resp.StatusCode, resp.Header, nil, err
	}
	if len(body) > maxResponseBytes {
		return resp.StatusCode, resp.Header, nil, fmt.Errorf("response exceeded %d bytes", maxResponseBytes)
	}
	return resp.StatusCode, resp.Header, body, nil
}

// backoff waits as long as the server asked, and otherwise doubles with jitter
// so that a batch of failures does not come back as one thundering herd.
func (c *Client) backoff(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		if retryAfter > maxRetryDelay {
			return maxRetryDelay
		}
		return retryAfter
	}
	delay := time.Duration(1<<uint(attempt)) * baseRetryDelay
	if delay <= 0 || delay > maxRetryDelay {
		delay = maxRetryDelay
	}
	return c.jitter(delay)
}

// jitter spreads a delay over plus or minus 20 percent.
func (c *Client) jitter(d time.Duration) time.Duration {
	return time.Duration(float64(d) * (0.8 + 0.4*c.rand()))
}

// parseRetryAfter reads the header in both of its documented forms: a count of
// seconds, or an HTTP date.
func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		if delay := when.Sub(now); delay > 0 {
			return delay
		}
	}
	return 0
}

// sleepWithContext waits, or gives up early when the context ends.
func sleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// snippet renders the start of an unexpected body on one line, so that an edge
// network's HTML page is readable in a log without flooding it.
func snippet(body []byte) string {
	const limit = 200
	if len(body) > 4*limit {
		body = body[:4*limit]
	}
	runes := []rune(strings.Join(strings.Fields(string(body)), " "))
	if len(runes) == 0 {
		return "(empty body)"
	}
	if len(runes) > limit {
		// Cut by code point; cutting by byte would split a multi-byte character down the middle.
		return string(runes[:limit]) + "..."
	}
	return string(runes)
}

// isLoopback reports whether a host is this machine.
func isLoopback(host string) bool {
	host = strings.ToLower(strings.Trim(host, "[]"))
	return host == "localhost" || host == "::1" || strings.HasPrefix(host, "127.")
}

// required rejects an empty string argument before it reaches the network.
func required(name, value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("popnow: %s is required", name)
	}
	return nil
}

// withQuery appends an encoded query to a path, or returns the path unchanged
// when nothing was set. The contract rejects empty query parameters, so an
// unset option must become "no such parameter" rather than "param=".
func withQuery(path string, query url.Values) string {
	if len(query) == 0 {
		return path
	}
	return path + "?" + query.Encode()
}

// setIfNotEmpty adds a query parameter only when it carries a value.
func setIfNotEmpty(query url.Values, name, value string) {
	if value != "" {
		query.Set(name, value)
	}
}
