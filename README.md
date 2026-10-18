<div align="center">
  <a href="https://popnow.ai">
    <picture>
      <source media="(prefers-color-scheme: dark)" srcset="https://raw.githubusercontent.com/popnow-ai/popnow-go/main/assets/popnow-wordmark-light.png">
      <source media="(prefers-color-scheme: light)" srcset="https://raw.githubusercontent.com/popnow-ai/popnow-go/main/assets/popnow-wordmark.png">
      <img alt="PopNow" src="https://raw.githubusercontent.com/popnow-ai/popnow-go/main/assets/popnow-wordmark.png" width="260">
    </picture>
  </a>

  <h1>PopNow Go SDK: AI Video, Image and Music Generation API for Go</h1>

  <p><strong>popnow-go</strong> is a zero-dependency Go client for the PopNow API that turns text, images and audio into AI videos, images, music and speech, using the same models, prices and credits as <a href="https://popnow.ai">popnow.ai</a>.</p>

  <p>
    <a href="LICENSE"><img alt="License: MIT" src="https://img.shields.io/badge/license-MIT-blue.svg"></a>
    <img alt="Go 1.23+" src="https://img.shields.io/badge/go-1.23%2B-00ADD8.svg?logo=go&logoColor=white">
    <a href="https://pkg.go.dev/github.com/popnow-ai/popnow-go"><img alt="Go Reference" src="https://pkg.go.dev/badge/github.com/popnow-ai/popnow-go.svg"></a>
    <a href="https://github.com/popnow-ai/popnow-go/actions/workflows/ci.yml"><img alt="CI" src="https://github.com/popnow-ai/popnow-go/actions/workflows/ci.yml/badge.svg"></a>
    <a href="https://popnow.ai"><img alt="Website: popnow.ai" src="https://img.shields.io/badge/site-popnow.ai-ff5a36.svg"></a>
    <a href="https://popnow.ai/docs"><img alt="Docs" src="https://img.shields.io/badge/docs-popnow.ai%2Fdocs-111111.svg"></a>
  </p>
</div>

PopNow is a browser-based AI studio for videos, images and music: image-to-video, text-to-video, AI images, music and speech, one-tap trending video effects and image styles. This package wraps its developer API (`https://api.popnow.ai/api/v1`) so you can call Kling, Seedance, Wan, Vidu, MiniMax, Nano Banana, GPT Image, Flux, Suno and more from Go. Credit packs, no subscription; failed runs are refunded. *Make it pop.*

- **Standard library only.** Go 1.23+, no third-party modules in your dependency graph.
- **Safe retries.** Back-off with jitter on `429` / `500` / `503`, honouring `Retry-After`, and only for requests that cannot charge twice: task submissions require an `Idempotency-Key`.
- **The whole task workflow.** Quote, create (optionally waiting server-side), poll with back-off, retry, purge, task history with a cursor iterator, usage and balance.
- **Files and the asset library.** Streamed three-step uploads, LoRA import from Hugging Face or Civitai, `popnow://f/` and `popnow://a/` URIs, saved assets and characters.
- **Webhook verification** for task callbacks and for Standard Webhooks account endpoints.
- **Contract-tested** against a pinned copy of the [published OpenAPI spec](https://popnow.ai/docs/openapi.yaml).

## Quickstart

**1. Install** (Go 1.23+):

```bash
go get github.com/popnow-ai/popnow-go
```

**2. Create an API key** in the [API console](https://popnow.ai/api/console/keys) and export it:

```bash
export POPNOW_API_KEY="sk-popnow-..."
```

**3. Generate an image:**

```go
package main

import (
	"context"
	"log"

	popnow "github.com/popnow-ai/popnow-go"
)

func main() {
	ctx := context.Background()
	client, err := popnow.New(popnow.Options{}) // reads POPNOW_API_KEY
	if err != nil {
		log.Fatal(err)
	}

	request := popnow.CreateTaskInput{
		Model: "alibaba/z-image-turbo/text-to-image",
		Input: map[string]any{"prompt": "A small red fox curled up in a teacup, soft morning light"},
	}
	// One key per piece of work, reused on every resend: a repeat returns the
	// original task instead of generating (and charging) twice.
	task, err := client.CreateTask(ctx, request, "fox-teacup-1", popnow.CreateTaskOptions{})
	if err != nil {
		log.Fatal(err)
	}

	final, err := client.WaitForTerminal(ctx, task.TaskID, popnow.WaitOptions{})
	if err != nil {
		log.Fatal(err)
	}
	log.Println(final.State, final.Credits, "credits")
	for _, asset := range final.ReadyAssets() {
		log.Println(asset.URL) // a signed link, valid for about 20 minutes
	}
}
```

The import path is the module path, so `go get` works straight from GitHub; no registry is involved.

## What it covers

| Area | Methods | Endpoint |
| --- | --- | --- |
| Models | `ListModels`, `GetModel` | `GET /api/v1/models`, `GET /api/v1/models/{model}` |
| Pricing | `Quote` | `POST /api/v1/jobs/quote` |
| Tasks | `CreateTask`, `GetTask`, `WaitForTerminal`, `RetryTask`, `PurgeTask` | `POST /jobs/createTask`, `GET /jobs/recordInfo`, `POST /jobs/retry`, `POST /jobs/purge` |
| History | `ListTasks`, `EachTask`, `Tasks` (iterator) | `GET /api/v1/jobs` |
| Account | `GetBalance`, `GetUsage` | `GET /api/v1/chat/credit`, `GET /api/v1/usage` |
| Files | `Upload`, `UploadReader`, `UploadFile`, `CreateUploadURL`, `CommitUpload`, `ImportLoRA`, `CreateDownloadURL` | `/common/upload-url`, `/files/{id}/commit`, `/files/import`, `/common/download-url` |
| Asset library | `CreateAsset`, `ListAssets`, `EachAsset`, `Assets` (iterator), `GetAsset`, `UpdateAsset`, `DeleteAsset`, `GetAssetUsage`, `ListCharacters`, `GetCharacter` | `/api/v1/assets*` |
| Webhooks | `VerifyWebhook`, `VerifyEndpointWebhook` | task callbacks and account endpoints |

Models include Kling, Seedance, Wan, Vidu, MiniMax, Nano Banana, GPT Image, Flux, Qwen Image, Seedream, Suno and more across text-to-image, image editing, text-to-video, image-to-video, reference-to-video, video extend, upscaling, text-to-speech, music and speech-to-text. The live list, with each model's input schema and credit price, is `ListModels`:

```go
models, err := client.ListModels(ctx, popnow.ListModelsOptions{Modality: "video", Task: "image-to-video"})
if err != nil {
	return err
}
for _, m := range models.Items {
	if m.Enabled && m.Available && m.Credits != nil {
		log.Printf("%s  %.1f credits %s", m.Model, m.Credits.PerUnit, m.Credits.Unit)
	}
}

// GetModel always includes the JSON Schema that validates Input.
schema, err := client.GetModel(ctx, "alibaba/wan-3.0/image-to-video")
if err != nil {
	return err
}
log.Println(string(schema.InputSchema))
```

## Show the price first

`Quote` runs the same validation as `CreateTask` but creates nothing and holds nothing. Pass its `QuoteID` and `MaxCredits` back to make sure the price you showed is the price that is held; a price that moved is refused with code `40901` instead.

```go
request := popnow.CreateTaskInput{
	Model: "alibaba/wan-3.0/image-to-video",
	Input: map[string]any{
		"image_url":        "https://media.popnow.ai/movie/lighthouse.webp",
		"prompt":           "Waves crash against the lighthouse, slow push in",
		"resolution":       "480p",
		"duration_seconds": 5,
	},
}

quote, err := client.Quote(ctx, request)
if err != nil {
	return err
}
log.Printf("at most %d credits ($%s)", quote.MaxCredits, quote.MaxCharge)

request.QuoteID = quote.QuoteID
request.ExpectedCredits = &quote.MaxCredits

key, err := popnow.NewIdempotencyKey() // store it with your job before sending
if err != nil {
	return err
}
task, err := client.CreateTask(ctx, request, key, popnow.CreateTaskOptions{})
if err != nil {
	return err
}
log.Println("accepted", task.TaskID, "holding", task.EstimatedCredits, "credits")
```

1 credit = $0.01. The hold is the ceiling: unused credits are released when the task settles, and failed or expired tasks release the hold in full.

## Skip the first poll

`WaitSeconds` asks the server to hold the connection open until the task finishes (up to 60 seconds), so a quick model returns its result with the submission.

```go
seconds := 30
task, err := client.CreateTask(ctx, request, key, popnow.CreateTaskOptions{WaitSeconds: &seconds})
if err != nil {
	return err
}

// Task is nil whenever the wait ran out. That is the ordinary outcome, not an
// error: the generation is still running, so poll from here.
final := task.Task
if final == nil {
	final, err = client.WaitForTerminal(ctx, task.TaskID, popnow.WaitOptions{})
	if err != nil {
		return err
	}
}
if final.State != popnow.StateSucceeded {
	log.Println("failed:", final.ErrorCode, final.ErrorMessage)
}
```

**It is an optimisation, never a guarantee**: always check `task.Task` for nil. A terminal state is not necessarily a success, either: `failed` and `expired` end the wait too, with `ErrorCode` set. `WaitForTerminal` returns `*popnow.WaitTimeoutError` when its local deadline passes; that is not a verdict, the task is still running, so keep the task ID and check again later.

## Start from a local file

Image-to-video, image editing and reference models need your material on PopNow first. `UploadFile` streams the file (ticket, `PUT` to storage, commit) and returns a `popnow://f/` URI for task input. A public HTTPS URL can be passed directly instead.

```go
uploaded, err := client.UploadFile(ctx, "/path/to/reference.png", "")
if err != nil {
	return err
}
request.Input["image_url"] = uploaded.URI // "popnow://f/fil_..."

// LoRA weights: upload a .safetensors file, or import one from a link.
lora, err := client.ImportLoRA(ctx, popnow.LoRAImport{URL: "owner/repo"})
if err != nil {
	return err
}
log.Println(lora.URI) // goes in loras[].path
```

Accepted: JPEG, PNG, WebP and GIF images (10 MiB), MP4/WebM video and MP3/WAV audio (90 MiB, 600 s), reference documents (PDF, Word, Excel, PowerPoint, Pages, Numbers, plain text, Markdown) and `.safetensors` LoRA weights (2 GiB). The content type is inferred from the extension, except Keynote: `.key` is also how private keys are named, so pass `"application/vnd.apple.keynote"` yourself.

## Keep results in the asset library

Output links expire after about 20 minutes. To keep a result, save it to the asset library: it gets a stable `popnow://a/` URI that works in any media field of later requests made with keys of the same workspace.

```go
first := 0
saved, err := client.CreateAsset(ctx, popnow.CreateAssetInput{
	Source: popnow.AssetSourceTaskOutput,
	TaskID: final.TaskID,
	Index:  &first,
	Name:   "Lighthouse clip",
}, "save-"+final.TaskID+"-0")
if err != nil {
	return err
}
log.Println(saved.URI) // "popnow://a/ast_..."

for asset, err := range client.Assets(ctx, popnow.ListAssetsOptions{Kind: "video"}) {
	if err != nil {
		return err
	}
	log.Println(asset.Name, asset.URI)
}
```

Storage unlocks with your lifetime credit purchases; `GetAssetUsage` shows the plan and limits. Without a top-up, saving fails with `APIError.Reason == "asset_library_locked"`.

## Webhooks instead of polling

Pass `CallBackURL` (public HTTPS) on `CreateTask` and PopNow posts the finished task to it. Verify every delivery against the **raw** body with your webhook signing secret from the [API console](https://popnow.ai/api/console/webhooks):

```go
package main

import (
	"io"
	"log"
	"net/http"
	"os"

	popnow "github.com/popnow-ai/popnow-go"
)

func main() {
	secret := os.Getenv("POPNOW_WEBHOOK_SECRET")
	http.HandleFunc("/hooks/popnow", func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(io.LimitReader(r.Body, 10<<20)) // the exact bytes
		if err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		delivery, err := popnow.VerifyWebhook(secret, r.Header, body, 0)
		if err != nil {
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK) // acknowledge within 15 seconds, then do the work
		// DeliveryID is stable across retries: store it to ignore duplicates.
		if delivery.Task != nil {
			log.Println(delivery.DeliveryID, delivery.TaskID, delivery.Task.State)
		}
	})
	log.Fatal(http.ListenAndServe(":8080", nil))
}
```

Account endpoints registered in the console use the [Standard Webhooks](https://www.standardwebhooks.com/) format and their own `whsec_` secret; verify those with `popnow.VerifyEndpointWebhook(secret, r.Header, body, 0)`, which also accepts the two signatures sent during a secret rotation. Both verifiers check the signature first and the timestamp second (five minutes by default).

## Balance, usage and history

All three are scoped to **this API key**: other keys on the account, and generations started on the website, are not included.

```go
balance, err := client.GetBalance(ctx)
if err != nil {
	return err
}
log.Printf("%d credits available, %d held", balance.AvailableCredits, balance.HeldCredits)

usage, err := client.GetUsage(ctx, popnow.UsageOptions{From: "2026-10-01", To: "2026-10-08"})
if err != nil {
	return err
}
log.Printf("%d calls, %d credits over [%s,%s)", usage.TotalCalls, usage.TotalCredits, usage.From, usage.To)

// Every filter is repeated on every page; only the cursor moves.
for item, err := range client.Tasks(ctx, popnow.ListTasksOptions{From: "2026-10-01", To: "2026-10-08"}) {
	if err != nil {
		return err
	}
	log.Println(item.TaskID, item.State, item.Credits)
}
```

Dates are UTC calendar dates in a half-open interval `[from, to)` of at most 92 days. Pin `From` and `To` on a walk that can cross midnight UTC: left empty, the server recomputes its window on every page. `GetUsage` is for reconciliation, not progress: it shares an account-wide limit of 30 requests a minute.

## Errors and retries

Every rejected call is a `*popnow.APIError`:

```go
var apiErr *popnow.APIError
if errors.As(err, &apiErr) {
	log.Println(apiErr.Status, apiErr.Code, apiErr.Reason, apiErr.RequestID, apiErr.RetryAfter)
	if apiErr.Code == popnow.CodeInsufficientCredits {
		log.Println("buy credits at https://popnow.ai/pricing")
	}
}
```

Branch on `Code`, never on the message: messages can be translated (`Options.AcceptLanguage`), codes are not.

| Code | HTTP | Meaning | What this client does / what to do |
| --- | --- | --- | --- |
| `400` | 400 | invalid request or input; `msg` names the problem | fix the input |
| `40003` | 400 | uploaded bytes do not match their ticket | upload again with a new ticket |
| `40004` | 400 | valid, but this combination of settings cannot be served now | change the named setting |
| `401` | 401 | missing, unknown or revoked key | check `POPNOW_API_KEY` |
| `40201` | 402 | not enough credits for the hold | buy credits, resend |
| `40202` | 402 | a key spend cap was reached | raise the cap in the console |
| `40301` / `40302` / `40303` | 403 | key not allowed this model / IP not allowed / region not offered | adjust the key |
| `40310` | 403 | account email not verified | open the verification link |
| `404` | 404 | not found, or another key's resource | - |
| `409` / `40901` | 409 | idempotency conflict / quote expired or price changed | new key / quote again |
| `429` | 429 | too many requests | retried automatically after `Retry-After` |
| `500` | 500 | server error | retried automatically with back-off |
| `503` / `50301` | 503 | dependency or model briefly unavailable | retried automatically |
| `50302` | 503 | a synchronous generation failed and was refunded | not retried: resend with a **new** idempotency key |

Automatic retries apply only to requests that are safe to repeat: reads, quotes, purges, and submissions that carry an idempotency key. Upload tickets and LoRA imports are never resent. Tune with `Options.MaxRetries` (`-1` disables), `RequestTimeout`, `UploadTimeout` and `WaitTimeout`.

A failed generation shows up as a task with `State == "failed"` and an `ErrorCode` such as `content_rejected`, `invalid_asset` or `timeout`, not as an error from `WaitForTerminal`.

## Configuration

| Option | Default |
| --- | --- |
| `APIKey` | `POPNOW_API_KEY` |
| `BaseURL` | `https://api.popnow.ai/api/v1` (HTTPS only; HTTP allowed for loopback) |
| `HTTPClient` | `&http.Client{}` (any `Do(*http.Request)` works, for proxies or tests) |
| `UserAgent` | `popnow-go/<version> (+https://github.com/popnow-ai/popnow-go)` |
| `AcceptLanguage` | none (English messages) |
| `RequestTimeout` / `UploadTimeout` / `WaitTimeout` | 30 s / 30 min / 10 min |
| `MaxRetries` | 3 |

Keep the API key on your server. Never ship it inside a binary you distribute, and never attach it to the signed output links.

## FAQ

### How do I generate a video from an image with an API in Go?

Upload the image with `client.UploadFile` (or use a public HTTPS URL), then call `client.CreateTask` with an image-to-video model such as `alibaba/wan-3.0/image-to-video` and `Input` containing `image_url`, `prompt`, `resolution` and `duration_seconds`. Wait with `client.WaitForTerminal` and read `ReadyAssets()[0].URL`.

### How much does a PopNow API call cost?

Each model has a credit price (1 credit = $0.01), shown by `ListModels` in `Model.Credits` and on [popnow.ai/pricing](https://popnow.ai/pricing). `client.Quote` returns the exact `MaxCredits` for one request without spending anything. API calls and the website share one wallet and one price list. Failed tasks are refunded.

### Which AI models can I call from Go?

Every image, video and audio model on PopNow: Kling, Seedance, Wan, Vidu, MiniMax, Nano Banana, GPT Image, Flux, Qwen Image, Seedream, Suno and more. `client.ListModels(ctx, popnow.ListModelsOptions{})` returns the live list with input schemas.

### How do I avoid paying twice when a request times out?

Pass the same idempotency key on every resend of the same piece of work. For 24 hours a repeat returns the original task instead of creating a new one. `CreateTask` requires a key for this reason, and the client's own automatic retries reuse it.

### Do I have to poll, or can PopNow call my server?

Either. Set `CallBackURL` on `CreateTask` and verify each delivery with `popnow.VerifyWebhook`; or set `CreateTaskOptions.WaitSeconds` to have the server hold the request open for quick models; or poll with `WaitForTerminal`, which backs off for you.

### How long are generated files kept?

Signed output links last about 20 minutes; read the task again or call `CreateDownloadURL` for a fresh one. Outputs themselves follow the retention policy shown in `Task.Retention` (you can shorten it per task with `CreateTaskOptions.RetentionSeconds`). To keep a result indefinitely, save it with `CreateAsset`.

### Is there an MCP server for ChatGPT or Claude?

Yes: a remote MCP server at `https://api.popnow.ai/mcp` exposes image, video, music and speech tools. See [popnow.ai/mcp](https://popnow.ai/mcp) and [popnow-mcp](https://github.com/popnow-ai/popnow-mcp).

## Development

```bash
gofmt -l .
go vet ./...
go test -race ./...
```

The tests use local fake servers and need no API key. `TestContractHasNotDrifted` compares `contracts/openapi.yaml` with the published spec (set `POPNOW_SKIP_LIVE_CONTRACT=1` offline), every Go block in this README is compiled by `readme_test.go`, and `POPNOW_LIVE_TESTS=1` runs one request against the real API with a fake key to check the `401` envelope.

## Links

- Website: [popnow.ai](https://popnow.ai)
- Docs: [popnow.ai/docs](https://popnow.ai/docs)
- API reference (OpenAPI): [popnow.ai/docs/openapi.yaml](https://popnow.ai/docs/openapi.yaml)
- Pricing: [popnow.ai/pricing](https://popnow.ai/pricing)
- MCP server: [popnow.ai/mcp](https://popnow.ai/mcp)
- llms.txt: [popnow.ai/llms.txt](https://popnow.ai/llms.txt)
- Other PopNow repos: [popnow-mcp](https://github.com/popnow-ai/popnow-mcp), [popnow-skills](https://github.com/popnow-ai/popnow-skills), [popnow-python](https://github.com/popnow-ai/popnow-python), [popnow-js](https://github.com/popnow-ai/popnow-js), [popnow-api](https://github.com/popnow-ai/popnow-api), [popnow-blender](https://github.com/popnow-ai/popnow-blender), [popnow-comfyui](https://github.com/popnow-ai/popnow-comfyui), [n8n-nodes-popnow](https://github.com/popnow-ai/n8n-nodes-popnow), [popnow-cli](https://github.com/popnow-ai/popnow-cli), [popnow-php](https://github.com/popnow-ai/popnow-php)

## Contributing

Issues and pull requests are welcome; see [CONTRIBUTING.md](CONTRIBUTING.md). To report a vulnerability, see [SECURITY.md](SECURITY.md).

## License

[MIT](LICENSE) © PopNow
