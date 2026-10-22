# Changelog

All notable changes to this module are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/).

## [Unreleased]

### Changed

- Docs and the pinned contract describe credits in the current unit (1 credit = $0.001 since 2026-10-19); test fixtures use matching credit and USD pairs. No code change: the SDK reads credit amounts as the API returns them.

## [0.1.0] - 2026-10-19

First release of the PopNow Go SDK (`github.com/popnow-ai/popnow-go`, package `popnow`).

### Added

- `New` with `POPNOW_API_KEY`, HTTPS-only base URL (loopback HTTP allowed), explicit
  `popnow-go/<version>` User-Agent, optional `Accept-Language`, per-attempt, upload and wait timeouts.
- Envelope decoding that branches on the business code, `*APIError` with `Code`, `Reason`
  (`data.reason`), `RequestID` and `RetryAfter`, and named constants for every documented code.
- Retries with jittered exponential back-off honouring `Retry-After`, applied only to requests that
  are safe to repeat; `50302` is never resent.
- Tasks: `Quote`, `CreateTask` (required idempotency key, `X-PopNow-Retention`, server-side `wait`),
  `GetTask`, `WaitForTerminal`, `RetryTask`, `PurgeTask`, `ListTasks`, `EachTask` and the `Tasks`
  iterator.
- Models: `ListModels`, `GetModel` with credit prices and input schemas.
- Account: `GetBalance`, `GetUsage` in credits and USD.
- Files: streamed three-step `Upload` / `UploadReader` / `UploadFile` (presigned URL redacted from
  errors), `ImportLoRA`, `CreateDownloadURL`.
- Asset library: `CreateAsset`, `ListAssets`, `EachAsset`, `Assets`, `GetAsset`, `UpdateAsset`,
  `DeleteAsset`, `GetAssetUsage`, `ListCharacters`, `GetCharacter`.
- Webhooks: `VerifyWebhook` for task callbacks and `VerifyEndpointWebhook` for Standard Webhooks
  account endpoints, both checking the signature before the timestamp.
- Tests: contract drift check against a pinned OpenAPI copy, README compilation, an English-only and
  naming guard, and an opt-in live `401` check.

[0.1.0]: https://github.com/popnow-ai/popnow-go/releases/tag/v0.1.0
