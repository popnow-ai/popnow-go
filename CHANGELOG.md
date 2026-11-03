# Changelog

## 0.3.0 — 2026-11-03

- Support the expanded live model catalog and embedding task outputs, including measured input usage.
- Preserve exact multimodal USD rate metadata and document native embedding/speech interfaces.


All notable changes to this module are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/).

## [Unreleased]

## [0.2.0] - 2026-11-01

The API bills in exact USD from its own API balance, separate from website credits. This release
reads and sends the USD fields; nothing is removed.

### Added

- `CreateTaskInput.ExpectedCostUsd` and `CreateTaskInput.WithQuote(quote)`, which confirms a quote
  by sending `quoteId` and `expectedCostUsd` = `Quote.MaxChargeUsd`. `ExpectedCostUsd` is checked
  locally against the contract's decimal format (up to nine decimal places, no exponent).
- `Quote.MaxChargeUsd`, `Quote.ExpectedChargeUsd` and `Quote.ExpectedCharge`.
- `CreateTaskResult.EstimatedCostUsd` and `Task.CostUsd`.
- `Balance.AvailableUsd`, `Balance.HeldUsd` and `Balance.TotalUsd`.
- `Model.Usd` (`ModelUsd`, `ModelUsdRule`, `ModelUsdLongContext`, `ModelPriceBlocks`): the model's
  USD rate snapshot with exact decimal strings.
- `CodeInsufficientBalance` for business code `40201`.

### Deprecated

- The integer credit fields: `CreateTaskInput.ExpectedCredits`, `Quote.MaxCredits`,
  `Quote.ExpectedCredits`, `CreateTaskResult.EstimatedCredits`, `Task.Credits`,
  `TaskListItem.Credits`, `Balance.AvailableCredits` / `HeldCredits` / `TotalCredits`,
  `UsageReport.TotalCredits`, `UsageDay.Credits`, `UsageModel.Credits`, `Model.Credits`,
  `ModelCredits` and `ModelCreditRule`. They still decode and are still sent when set.
- `CreateTaskInput.ExpectedCost`, the legacy name of `ExpectedCostUsd`.
- `CodeInsufficientCredits`, now an alias of `CodeInsufficientBalance`.

### Changed

- README, package docs and `llms.txt` use USD throughout: quotes, holds, charges, balance and
  usage, with the API balance funded in API billing. Links point to the developer MCP server
  (`https://api.popnow.ai/api/v1/mcp`, popnow.ai/api/mcp, popnow-api-mcp), the developer skills
  page (popnow.ai/api/skills) and API pricing (popnow.ai/api/pricing).
- The pinned contract is refreshed to the current published spec. It leaves out the catalogue
  lines the repository guard does not allow, and the live drift check filters the published copy
  the same way before comparing.
- Docs and the pinned contract describe credits in the current unit (1 credit = $0.001 since
  2026-10-19); test fixtures use matching credit and USD pairs.

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

[0.2.0]: https://github.com/popnow-ai/popnow-go/releases/tag/v0.2.0
[0.1.0]: https://github.com/popnow-ai/popnow-go/releases/tag/v0.1.0
