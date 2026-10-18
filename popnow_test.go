package popnow

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// These tests cover what PopNow adds on top of the shared task workflow: credits, the asset
// library, LoRA import, streamed uploads, account endpoint webhooks and data.reason.

// capturedRequest is one request seen by captureAPI.
type capturedRequest struct {
	Method string
	URI    string
	Header http.Header
	Body   string
}

// captureAPI answers each request with the next canned body (or an empty success) and records
// everything about it, headers and body included.
type captureAPI struct {
	server    *httptest.Server
	mu        sync.Mutex
	requests  []capturedRequest
	responses []string
	statuses  []int
}

func newCaptureAPI(t *testing.T, responses ...string) *captureAPI {
	t.Helper()
	api := &captureAPI{responses: responses}
	api.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		api.mu.Lock()
		api.requests = append(api.requests, capturedRequest{
			Method: r.Method, URI: r.URL.RequestURI(), Header: r.Header.Clone(), Body: string(raw),
		})
		n := len(api.requests) - 1
		body := `{"code":200,"msg":"success","request_id":"r","data":{}}`
		if n < len(api.responses) {
			body = api.responses[n]
		}
		status := http.StatusOK
		if n < len(api.statuses) {
			status = api.statuses[n]
		}
		api.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(api.server.Close)
	return api
}

func (a *captureAPI) seen() []capturedRequest {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]capturedRequest(nil), a.requests...)
}

func (a *captureAPI) client(t *testing.T, opts Options) *Client {
	t.Helper()
	opts.BaseURL = a.server.URL
	if opts.APIKey == "" {
		opts.APIKey = "sk-popnow-test"
	}
	// Keep retries fast and deterministic.
	if opts.Sleep == nil {
		opts.Sleep = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
	}
	client, err := New(opts)
	if err != nil {
		t.Fatalf("build a client: %v", err)
	}
	return client
}

// -- Construction -----------------------------------------------------------

func TestNewReadsTheKeyFromTheEnvironment(t *testing.T) {
	t.Setenv(APIKeyEnv, "sk-popnow-from-env")
	api := newCaptureAPI(t)
	client, err := New(Options{BaseURL: api.server.URL})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := client.GetBalance(context.Background()); err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	if got := api.seen()[0].Header.Get("Authorization"); got != "Bearer sk-popnow-from-env" {
		t.Errorf("Authorization = %q", got)
	}
}

func TestNewRefusesAMissingKeyAndPlainHTTP(t *testing.T) {
	t.Setenv(APIKeyEnv, "")
	if _, err := New(Options{}); err == nil || !strings.Contains(err.Error(), APIKeyEnv) {
		t.Errorf("a missing key must fail and name %s, got %v", APIKeyEnv, err)
	}
	if _, err := New(Options{APIKey: "k", BaseURL: "http://api.example.com/api/v1"}); err == nil {
		t.Error("plain HTTP to a remote host would send the key in the clear")
	}
	if _, err := New(Options{APIKey: "k", BaseURL: "http://127.0.0.1:8080/api/v1"}); err != nil {
		t.Errorf("loopback HTTP is local development and must be allowed: %v", err)
	}
}

func TestOptionalHeadersAreSentOnlyWhenConfigured(t *testing.T) {
	api := newCaptureAPI(t)
	if _, err := api.client(t, Options{}).GetBalance(context.Background()); err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	if _, err := api.client(t, Options{AcceptLanguage: "de", UserAgent: "my-tool/2.0"}).
		GetBalance(context.Background()); err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	seen := api.seen()
	if _, present := seen[0].Header["Accept-Language"]; present {
		t.Error("Accept-Language was sent although none was configured")
	}
	if seen[0].Header.Get("User-Agent") != UserAgent {
		t.Errorf("default User-Agent = %q", seen[0].Header.Get("User-Agent"))
	}
	if seen[1].Header.Get("Accept-Language") != "de" || seen[1].Header.Get("User-Agent") != "my-tool/2.0" {
		t.Errorf("configured headers = %v", seen[1].Header)
	}
}

// -- Errors and retries -----------------------------------------------------

func TestAPIErrorCarriesTheMachineReadableReason(t *testing.T) {
	api := newCaptureAPI(t, `{"code":400,"msg":"Top up to unlock the asset library",
		"request_id":"req_lock","data":{"reason":"asset_library_locked"}}`)
	api.statuses = []int{http.StatusBadRequest}

	_, err := api.client(t, Options{}).CreateAsset(context.Background(), CreateAssetInput{
		Source: AssetSourceUpload, FileID: "fil_1",
	}, "save-fil_1")

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("want *APIError, got %T: %v", err, err)
	}
	if apiErr.Code != CodeBadRequest || apiErr.Reason != "asset_library_locked" || apiErr.RequestID != "req_lock" {
		t.Errorf("error = %+v", apiErr)
	}
	if !strings.Contains(err.Error(), "reason asset_library_locked") {
		t.Errorf("the reason is missing from the message: %v", err)
	}
}

// TestRefundedGenerationIsNotResent pins that 50302 is surfaced rather than replayed: its old
// idempotency key would replay the recorded failure, so resending can never succeed.
func TestRefundedGenerationIsNotResent(t *testing.T) {
	api := newCaptureAPI(t, `{"code":50302,"msg":"generation failed, refunded","request_id":"r"}`)
	api.statuses = []int{http.StatusServiceUnavailable}

	_, err := createWithClient(api.client(t, Options{}))
	var apiErr *APIError
	if !errors.As(err, &apiErr) || !apiErr.Refunded() || apiErr.Retryable() {
		t.Fatalf("want a refunded, non-retryable *APIError, got %v", err)
	}
	if got := len(api.seen()); got != 1 {
		t.Errorf("a refunded generation was sent %d times", got)
	}
}

// TestDependencyOutageIsRetriedUnderTheSameKey is the converse: 503 with the same bytes is safe
// because the idempotency key folds a duplicate back onto the original task.
func TestDependencyOutageIsRetriedUnderTheSameKey(t *testing.T) {
	api := newCaptureAPI(t,
		`{"code":503,"msg":"try again","request_id":"r1"}`,
		`{"code":200,"msg":"success","request_id":"r2","data":{"taskId":"job_1","state":"queued"}}`)
	api.statuses = []int{http.StatusServiceUnavailable, http.StatusAccepted}

	var slept []time.Duration
	client := api.client(t, Options{Sleep: func(ctx context.Context, d time.Duration) error {
		slept = append(slept, d)
		return nil
	}})
	result, err := createWithClient(client)
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	seen := api.seen()
	if result.TaskID != "job_1" || len(seen) != 2 {
		t.Fatalf("result = %+v after %d requests", result, len(seen))
	}
	if seen[0].Header.Get("Idempotency-Key") != seen[1].Header.Get("Idempotency-Key") {
		t.Error("the retry changed the idempotency key, which would create a second task")
	}
	if len(slept) != 1 || slept[0] <= 0 {
		t.Errorf("the retry did not back off: %v", slept)
	}
}

// TestUploadTicketIsNeverRetried pins that a ticket request is not replayable: each one allocates
// a new file ID, so a retry only leaves an orphan ticket behind.
func TestUploadTicketIsNeverRetried(t *testing.T) {
	api := newCaptureAPI(t, `{"code":500,"msg":"boom","request_id":"r"}`)
	api.statuses = []int{http.StatusInternalServerError}
	if _, err := api.client(t, Options{}).CreateUploadURL(context.Background(), "image/png", 10); err == nil {
		t.Fatal("expected the fake API's rejection")
	}
	if got := len(api.seen()); got != 1 {
		t.Errorf("the upload ticket request was sent %d times", got)
	}
}

func createWithClient(client *Client) (*CreateTaskResult, error) {
	return client.CreateTask(context.Background(), CreateTaskInput{
		Model: "alibaba/z-image-turbo/text-to-image", Input: map[string]any{"prompt": "p"},
	}, "order-8814-render-1", CreateTaskOptions{})
}

// -- Quote and createTask bodies ---------------------------------------------

func TestQuoteSendsOnlyThePricedFields(t *testing.T) {
	api := newCaptureAPI(t, `{"code":200,"msg":"success","request_id":"r","data":{
		"quoteId":"q_1","model":"m","estimatedCost":"0.15","maxCharge":"0.15","maxCredits":15,
		"expectedCredits":15,"currency":"USD","quantity":"1","unit":"per_image",
		"expiresAt":"2030-01-01T00:00:00Z"}}`)
	previous := int64(9)
	quote, err := api.client(t, Options{}).Quote(context.Background(), CreateTaskInput{
		Model: "m", Input: map[string]any{"prompt": "p"}, QuoteID: "q_old", ExpectedCredits: &previous,
	})
	if err != nil {
		t.Fatalf("Quote: %v", err)
	}
	if quote.MaxCredits != 15 || quote.ExpectedCredits != 15 || quote.QuoteID != "q_1" {
		t.Errorf("quote = %+v", quote)
	}
	body := api.seen()[0].Body
	if strings.Contains(body, "q_old") || strings.Contains(body, "expectedCredits") {
		t.Errorf("a quote request carried an old price confirmation: %s", body)
	}
}

// TestExpectedCreditsZeroIsSent pins why ExpectedCredits is a pointer: 0 is a price that can be
// confirmed, and an int with omitempty would silently drop it.
func TestExpectedCreditsZeroIsSent(t *testing.T) {
	api := newCaptureAPI(t, `{"code":200,"msg":"success","request_id":"r","data":{"taskId":"t","state":"queued"}}`)
	zero := int64(0)
	if _, err := api.client(t, Options{}).CreateTask(context.Background(), CreateTaskInput{
		Model: "m", Input: map[string]any{"prompt": "p"}, QuoteID: "q_1", ExpectedCredits: &zero,
	}, "k-1", CreateTaskOptions{}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	var sent map[string]any
	if err := json.Unmarshal([]byte(api.seen()[0].Body), &sent); err != nil {
		t.Fatalf("decode the body: %v", err)
	}
	if v, ok := sent["expectedCredits"]; !ok || v.(float64) != 0 {
		t.Errorf("expectedCredits = %v (present %v), want 0", v, ok)
	}
}

func TestLocalValidationStopsBadRequestsBeforeTheNetwork(t *testing.T) {
	api := newCaptureAPI(t)
	client := api.client(t, Options{})
	ctx := context.Background()
	input := CreateTaskInput{Model: "m", Input: map[string]any{"prompt": "p"}}

	cases := map[string]func() error{
		"plain http callback": func() error {
			bad := input
			bad.CallBackURL = "http://example.com/hook"
			_, err := client.CreateTask(ctx, bad, "k", CreateTaskOptions{})
			return err
		},
		"missing idempotency key": func() error {
			_, err := client.CreateTask(ctx, input, "  ", CreateTaskOptions{})
			return err
		},
		"oversized idempotency key": func() error {
			_, err := client.CreateTask(ctx, input, strings.Repeat("k", 129), CreateTaskOptions{})
			return err
		},
		"missing model": func() error {
			_, err := client.Quote(ctx, CreateTaskInput{Input: input.Input})
			return err
		},
		"missing input": func() error {
			_, err := client.Quote(ctx, CreateTaskInput{Model: "m"})
			return err
		},
		"unsupported upload type": func() error {
			_, err := client.CreateUploadURL(ctx, "image/svg+xml", 10)
			return err
		},
		"empty upload": func() error {
			_, err := client.Upload(ctx, nil, "image/png")
			return err
		},
		"empty task id": func() error {
			_, err := client.GetTask(ctx, "")
			return err
		},
		"asset without source": func() error {
			_, err := client.CreateAsset(ctx, CreateAssetInput{}, "k")
			return err
		},
		"task output without key or index": func() error {
			_, err := client.CreateAsset(ctx, CreateAssetInput{Source: AssetSourceTaskOutput, TaskID: "job_1"}, "k")
			return err
		},
		"asset without idempotency key": func() error {
			_, err := client.CreateAsset(ctx, CreateAssetInput{Source: AssetSourceUpload, FileID: "fil_1"}, "")
			return err
		},
		"empty asset update": func() error {
			_, err := client.UpdateAsset(ctx, "ast_1", UpdateAssetInput{})
			return err
		},
		"lora without url": func() error {
			_, err := client.ImportLoRA(ctx, LoRAImport{})
			return err
		},
	}
	for name, call := range cases {
		if err := call(); err == nil {
			t.Errorf("%s: expected a local error", name)
		}
	}
	if got := api.seen(); len(got) != 0 {
		t.Errorf("invalid requests reached the network: %v", got)
	}
}

// -- Uploads and LoRA import ------------------------------------------------

// TestUploadFileStreamsWithAFixedLength proves the three-step upload against a fake storage
// server: the PUT carries an exact Content-Length (presigned PUTs refuse chunked bodies), the
// ticket's headers, and never the API key.
func TestUploadFileStreamsWithAFixedLength(t *testing.T) {
	payload := []byte("safetensors weights, pretend")
	var put capturedRequest
	var contentLength int64
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		put = capturedRequest{Method: r.Method, URI: r.URL.RequestURI(), Header: r.Header.Clone(), Body: string(raw)}
		contentLength = r.ContentLength
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(storage.Close)

	api := newCaptureAPI(t,
		`{"code":200,"msg":"success","request_id":"r","data":{"fileId":"fil_9","key":"popnow://f/fil_9",
			"uploadUrl":"`+storage.URL+`/put/fil_9?sig=abc","method":"PUT",
			"headers":{"Content-Type":"application/octet-stream","Content-Length":"`+strconv.Itoa(len(payload))+`"},
			"expiresAt":"2030-01-01T00:00:00Z","maxBytes":2147483648}}`,
		`{"code":200,"msg":"success","request_id":"r","data":{"fileId":"fil_9","status":"ready",
			"bytes":`+strconv.Itoa(len(payload))+`,"contentType":"application/octet-stream",
			"sha256":"`+strings.Repeat("a", 64)+`","uri":"popnow://f/fil_9","expiresAt":"2030-01-01T00:00:00Z"}}`)

	path := filepath.Join(t.TempDir(), "style.safetensors")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	uploaded, err := api.client(t, Options{}).UploadFile(context.Background(), path, "")
	if err != nil {
		t.Fatalf("UploadFile: %v", err)
	}
	if uploaded.URI != "popnow://f/fil_9" {
		t.Errorf("uri = %q", uploaded.URI)
	}

	seen := api.seen()
	if len(seen) != 2 || seen[0].URI != "/common/upload-url" || seen[1].URI != "/files/fil_9/commit" {
		t.Fatalf("api requests = %+v", seen)
	}
	if !strings.Contains(seen[0].Body, `"contentType":"application/octet-stream"`) ||
		!strings.Contains(seen[0].Body, `"bytes":`+strconv.Itoa(len(payload))) {
		t.Errorf("ticket body = %s", seen[0].Body)
	}
	if put.Method != http.MethodPut || put.Body != string(payload) || contentLength != int64(len(payload)) {
		t.Errorf("storage saw %s with %d bytes (Content-Length %d)", put.Method, len(put.Body), contentLength)
	}
	if put.Header.Get("Authorization") != "" {
		t.Error("the API key was sent to object storage")
	}
	if put.Header.Get("Content-Type") != "application/octet-stream" {
		t.Errorf("ticket header was not forwarded: %v", put.Header)
	}
	if len(put.Header.Values("Transfer-Encoding")) != 0 {
		t.Errorf("the upload went out chunked: %v", put.Header)
	}
}

func TestImportLoRAIsSentOnceWithItsToken(t *testing.T) {
	api := newCaptureAPI(t, `{"code":500,"msg":"boom","request_id":"r"}`)
	api.statuses = []int{http.StatusInternalServerError}

	_, err := api.client(t, Options{}).ImportLoRA(context.Background(), LoRAImport{
		URL: "owner/repo", Token: "hf_secret",
	})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != CodeServerError {
		t.Fatalf("want the fake API's rejection, got %v", err)
	}
	seen := api.seen()
	if len(seen) != 1 {
		t.Fatalf("an import was sent %d times; each success stores another copy", len(seen))
	}
	if seen[0].Method != http.MethodPost || seen[0].URI != "/files/import" ||
		seen[0].Body != `{"url":"owner/repo","token":"hf_secret"}` {
		t.Errorf("request = %+v", seen[0])
	}
	if strings.Contains(err.Error(), "hf_secret") {
		t.Errorf("the download token leaked into the error: %v", err)
	}
}

// -- Asset library ----------------------------------------------------------

const assetJSON = `{"id":"ast_1","uri":"popnow://a/ast_1","name":"Fox","kind":"image",
	"contentType":"image/png","bytes":1024,"width":512,"height":512,"source":"task_output",
	"sourceId":"job_1","createdAt":"2026-10-01T00:00:00Z","updatedAt":"2026-10-01T00:00:00Z",
	"previewUrl":"https://cdn.example/p","favorite":true,"model":"m","modelName":"M"}`

func TestListAssetsSendsEveryFilter(t *testing.T) {
	api := newCaptureAPI(t, `{"code":200,"msg":"success","request_id":"r","data":{
		"items":[`+assetJSON+`],"nextCursor":"c2"}}`)
	notFavorite := false
	list, err := api.client(t, Options{}).ListAssets(context.Background(), ListAssetsOptions{
		Kind: "image", Sources: []string{AssetSourceUpload, AssetSourceURL}, Q: "fox",
		Favorite: &notFavorite, CollectionID: "col_1", EntityID: "ent_1", Sort: "name", Limit: 10,
	})
	if err != nil {
		t.Fatalf("ListAssets: %v", err)
	}
	want := "/assets?collectionId=col_1&entityId=ent_1&favorite=false&kind=image&limit=10&q=fox&sort=name&source=upload%2Curl"
	if got := api.seen()[0].URI; got != want {
		t.Errorf("uri = %s\n want %s", got, want)
	}
	if len(list.Items) != 1 || list.Items[0].URI != "popnow://a/ast_1" || !list.Items[0].Favorite || list.NextCursor != "c2" {
		t.Errorf("list = %+v", list)
	}
}

func TestEachAssetFollowsTheCursorAndTheIteratorStops(t *testing.T) {
	page := func(id, cursor string) string {
		next := ""
		if cursor != "" {
			next = `,"nextCursor":"` + cursor + `"`
		}
		return `{"code":200,"msg":"success","request_id":"r","data":{"items":[` +
			strings.Replace(assetJSON, "ast_1", id, 2) + `]` + next + `}}`
	}
	api := newCaptureAPI(t, page("ast_1", "c2"), page("ast_2", ""))
	client := api.client(t, Options{})

	var ids []string
	for asset, err := range client.Assets(context.Background(), ListAssetsOptions{Sort: "oldest"}) {
		if err != nil {
			t.Fatalf("Assets: %v", err)
		}
		ids = append(ids, asset.ID)
	}
	if strings.Join(ids, ",") != "ast_1,ast_2" {
		t.Errorf("walked %v", ids)
	}
	seen := api.seen()
	if len(seen) != 2 || seen[1].URI != "/assets?cursor=c2&sort=oldest" {
		t.Errorf("requests = %+v", seen)
	}

	// Breaking out of the loop must not fetch another page.
	api2 := newCaptureAPI(t, page("ast_1", "c2"))
	for range api2.client(t, Options{}).Assets(context.Background(), ListAssetsOptions{}) {
		break
	}
	if got := len(api2.seen()); got != 1 {
		t.Errorf("breaking out still made %d requests", got)
	}
}

func TestCreateAssetFromTheFirstOutput(t *testing.T) {
	api := newCaptureAPI(t, `{"code":200,"msg":"success","request_id":"r","data":`+assetJSON+`}`)
	first := 0
	asset, err := api.client(t, Options{}).CreateAsset(context.Background(), CreateAssetInput{
		Source: AssetSourceTaskOutput, TaskID: "job_1", Index: &first, Name: "Fox",
	}, "save-job_1-0")
	if err != nil {
		t.Fatalf("CreateAsset: %v", err)
	}
	seen := api.seen()[0]
	if seen.Method != http.MethodPost || seen.URI != "/assets" || seen.Header.Get("Idempotency-Key") != "save-job_1-0" {
		t.Errorf("request = %+v", seen)
	}
	// Index 0 is the first output; dropping it would make the request ambiguous.
	if seen.Body != `{"source":"task_output","taskId":"job_1","index":0,"name":"Fox"}` {
		t.Errorf("body = %s", seen.Body)
	}
	if asset.ID != "ast_1" {
		t.Errorf("asset = %+v", asset)
	}
}

func TestAssetReadUpdateDeleteAndCharacters(t *testing.T) {
	api := newCaptureAPI(t,
		`{"code":200,"msg":"success","request_id":"r","data":`+assetJSON+`}`,
		`{"code":200,"msg":"success","request_id":"r","data":`+assetJSON+`}`,
		`{"code":200,"msg":"success","request_id":"r","data":{"id":"ast_1","deleted":true}}`,
		`{"code":200,"msg":"success","request_id":"r","data":{"tier":{"id":"free","name":"Free",
			"minTopupUsd":"0","maxBytes":0,"maxCount":0,"maxFileBytes":0},"usage":{"count":0,"bytes":0},
			"limits":{"tier":"free","maxCount":0,"maxBytes":0,"maxFileBytes":0,"globalMaxBytes":0,"nameMaxChars":80},
			"full":true,"nextTier":null,"tiers":[],"dailyCreateLimit":50,"createdToday":0,
			"scope":{"workspaceId":"ws_1","personal":true,"role":"owner","canWrite":true,"canTopUp":true}}}`,
		`{"code":200,"msg":"success","request_id":"r","data":{"items":[{"id":"ent_1","kind":"character",
			"name":"Mia","note":"","coverAssetId":"","cover":null,"assets":[],"count":0,
			"createdAt":"2026-10-01T00:00:00Z","updatedAt":"2026-10-01T00:00:00Z"}]}}`,
		`{"code":200,"msg":"success","request_id":"r","data":{"id":"ent_1","kind":"character",
			"name":"Mia","note":"","coverAssetId":"ast_1","cover":`+assetJSON+`,"assets":[`+assetJSON+`],"count":1,
			"createdAt":"2026-10-01T00:00:00Z","updatedAt":"2026-10-01T00:00:00Z"}}`)
	client := api.client(t, Options{})
	ctx := context.Background()

	if _, err := client.GetAsset(ctx, "ast_1"); err != nil {
		t.Fatalf("GetAsset: %v", err)
	}
	name := "Fox in a cup"
	if _, err := client.UpdateAsset(ctx, "ast_1", UpdateAssetInput{Name: &name}); err != nil {
		t.Fatalf("UpdateAsset: %v", err)
	}
	deleted, err := client.DeleteAsset(ctx, "ast_1")
	if err != nil || !deleted.Deleted {
		t.Fatalf("DeleteAsset: %+v, %v", deleted, err)
	}
	quota, err := client.GetAssetUsage(ctx)
	if err != nil {
		t.Fatalf("GetAssetUsage: %v", err)
	}
	if quota.NextTier != nil || !quota.Full || quota.Limits.NameMaxChars != 80 || !quota.Scope.CanTopUp {
		t.Errorf("quota = %+v", quota)
	}
	characters, err := client.ListCharacters(ctx, ListCharactersOptions{AssetID: "ast_1", Limit: 5})
	if err != nil {
		t.Fatalf("ListCharacters: %v", err)
	}
	if len(characters.Items) != 1 || characters.Items[0].Cover != nil {
		t.Errorf("characters = %+v", characters)
	}
	character, err := client.GetCharacter(ctx, "ent_1")
	if err != nil {
		t.Fatalf("GetCharacter: %v", err)
	}
	if character.Cover == nil || len(character.Assets) != 1 || character.Assets[0].URI != "popnow://a/ast_1" {
		t.Errorf("character = %+v", character)
	}

	want := []string{
		"GET /assets/ast_1",
		"PATCH /assets/ast_1",
		"DELETE /assets/ast_1",
		"GET /assets/usage",
		"GET /assets/entities?assetId=ast_1&limit=5",
		"GET /assets/entities/ent_1",
	}
	seen := api.seen()
	for i, w := range want {
		if i >= len(seen) || seen[i].Method+" "+seen[i].URI != w {
			t.Errorf("request #%d = %v, want %s", i+1, seen, w)
		}
	}
	// A rename sends only the name: favorite is left alone, not reset to false.
	if seen[1].Body != `{"name":"Fox in a cup"}` {
		t.Errorf("update body = %s", seen[1].Body)
	}
}

// -- Task iterator ----------------------------------------------------------

func TestTasksIteratorYieldsTheFailedPage(t *testing.T) {
	api := newCaptureAPI(t, `{"code":401,"msg":"Invalid API key","request_id":"r"}`)
	api.statuses = []int{http.StatusUnauthorized}

	var errs int
	for _, err := range api.client(t, Options{}).Tasks(context.Background(), ListTasksOptions{}) {
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Code != CodeUnauthorized {
			t.Errorf("want a 401 *APIError, got %v", err)
		}
		errs++
	}
	if errs != 1 {
		t.Errorf("the iterator yielded %d errors, want exactly one", errs)
	}
}

// -- Webhooks ---------------------------------------------------------------

// TestWebhookVersion2SignsTheDataTaskID pins where the task ID comes from in the current payload:
// data.taskId. A top-level task_id in a version 2 body is not what was signed.
func TestWebhookVersion2SignsTheDataTaskID(t *testing.T) {
	const secret = "callback-secret"
	body := []byte(`{"code":200,"msg":"success","data":{"taskId":"job_real","model":"m",` +
		`"state":"succeeded","credits":34,"cost":"0.34","settled":true,` +
		`"output":{"assets":[{"url":"https://cdn.example/a.mp4","mime":"video/mp4"}]}},"request_id":"whd_1"}`)
	now := strconv.FormatInt(time.Now().Unix(), 10)

	delivery, err := VerifyWebhook(secret, webhookHeader("2", now, signWebhook(t, secret, "job_real", now, body)), body, 0)
	if err != nil {
		t.Fatalf("VerifyWebhook: %v", err)
	}
	if delivery.TaskID != "job_real" || delivery.DeliveryID != "whd_1" || delivery.PayloadVersion != "2" {
		t.Errorf("delivery = %+v", delivery)
	}
	if delivery.Task == nil || delivery.Task.Credits != 34 || len(delivery.Task.ReadyAssets()) != 1 {
		t.Errorf("task = %+v", delivery.Task)
	}

	// The same body signed over a different task ID must fail.
	forged := webhookHeader("2", now, signWebhook(t, secret, "job_other", now, body))
	if _, err := VerifyWebhook(secret, forged, body, 0); !errors.Is(err, ErrWebhookSignature) {
		t.Errorf("a signature over another task ID verified: %v", err)
	}
	// A body changed after signing must fail too: the digest covers every byte.
	tampered := []byte(strings.Replace(string(body), `"credits":34`, `"credits":3400`, 1))
	if _, err := VerifyWebhook(secret, webhookHeader("2", now, signWebhook(t, secret, "job_real", now, body)), tampered, 0); !errors.Is(err, ErrWebhookSignature) {
		t.Errorf("a tampered body verified: %v", err)
	}
	if _, err := VerifyWebhook(secret, webhookHeader("3", now, "x"), body, 0); !errors.Is(err, ErrWebhookRejected) {
		t.Errorf("an unknown payload version was accepted: %v", err)
	}
}

func signEndpoint(key []byte, id, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(id + "." + timestamp + "."))
	mac.Write(body)
	return "v1," + base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func endpointHeader(id, timestamp, signature string) http.Header {
	header := http.Header{}
	header.Set("webhook-id", id)
	header.Set("webhook-timestamp", timestamp)
	header.Set("webhook-signature", signature)
	return header
}

func TestEndpointWebhookFollowsStandardWebhooks(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	secret := "whsec_" + base64.StdEncoding.EncodeToString(key)
	oldKey := []byte("fedcba9876543210fedcba9876543210")
	body := []byte(`{"type":"task.succeeded","timestamp":"2026-10-19T00:00:00Z",` +
		`"data":{"object":{"taskId":"job_1","model":"m","state":"succeeded","credits":12}}}`)
	now := strconv.FormatInt(time.Now().Unix(), 10)

	// During a rotation both the old and the new secret sign; either match is enough.
	signatures := signEndpoint(oldKey, "msg_1", now, body) + " " + signEndpoint(key, "msg_1", now, body)
	event, err := VerifyEndpointWebhook(secret, endpointHeader("msg_1", now, signatures), body, 0)
	if err != nil {
		t.Fatalf("VerifyEndpointWebhook: %v", err)
	}
	if event.ID != "msg_1" || event.Type != "task.succeeded" || event.Data.Object == nil ||
		event.Data.Object.TaskID != "job_1" || event.Data.Object.Credits != 12 || string(event.Raw) != string(body) {
		t.Errorf("event = %+v", event)
	}

	// The key is the decoded secret, not the secret string: signing with the string must fail.
	wrong := signEndpoint([]byte(secret), "msg_1", now, body)
	if _, err := VerifyEndpointWebhook(secret, endpointHeader("msg_1", now, wrong), body, 0); !errors.Is(err, ErrWebhookSignature) {
		t.Errorf("a signature made with the undecoded secret verified: %v", err)
	}
	// The ID is part of the signed message, so a replay under another ID fails.
	if _, err := VerifyEndpointWebhook(secret, endpointHeader("msg_2", now, signEndpoint(key, "msg_1", now, body)), body, 0); !errors.Is(err, ErrWebhookSignature) {
		t.Errorf("a signature moved to another webhook-id verified: %v", err)
	}
	// Signature first, freshness second, exactly as for task callbacks.
	stale := strconv.FormatInt(time.Now().Add(-time.Hour).Unix(), 10)
	if _, err := VerifyEndpointWebhook(secret, endpointHeader("msg_1", stale, signEndpoint(key, "msg_1", stale, body)), body, 0); !errors.Is(err, ErrWebhookStale) {
		t.Errorf("an authentic but stale delivery was not called stale: %v", err)
	}
	if _, err := VerifyEndpointWebhook(secret, endpointHeader("msg_1", stale, "v1,AAAA"), body, 0); !errors.Is(err, ErrWebhookSignature) || errors.Is(err, ErrWebhookStale) {
		t.Errorf("a forged stale delivery must be a signature failure: %v", err)
	}
	if _, err := VerifyEndpointWebhook("whsec_not base64!", endpointHeader("msg_1", now, signatures), body, 0); !errors.Is(err, ErrWebhookRejected) {
		t.Errorf("a malformed secret was accepted: %v", err)
	}
}

// -- WaitForTerminal waits for pending assets ---------------------------------

func TestWaitForTerminalWaitsForPendingAssets(t *testing.T) {
	var polls atomic.Int64
	api := newCaptureAPI(t,
		`{"code":200,"msg":"success","request_id":"r","data":{"taskId":"job_1","model":"m","state":"succeeded",
			"cost":"0.1","credits":10,"settled":true,"createdAt":"2026-10-01T00:00:00Z",
			"output":{"assets":[{"pending":true}]}}}`,
		`{"code":200,"msg":"success","request_id":"r","data":{"taskId":"job_1","model":"m","state":"succeeded",
			"cost":"0.1","credits":10,"settled":true,"createdAt":"2026-10-01T00:00:00Z",
			"output":{"assets":[{"key":"a","url":"https://cdn.example/a.png"}]}}}`)
	client := api.client(t, Options{Sleep: func(ctx context.Context, _ time.Duration) error {
		polls.Add(1)
		return ctx.Err()
	}})
	task, err := client.WaitForTerminal(context.Background(), "job_1", WaitOptions{Timeout: time.Second})
	if err != nil {
		t.Fatalf("WaitForTerminal: %v", err)
	}
	if len(task.ReadyAssets()) != 1 || polls.Load() != 1 {
		t.Errorf("returned %+v after %d sleeps", task.Output, polls.Load())
	}
}
