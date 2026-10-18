# Contributing

Thanks for helping improve the PopNow Go SDK.

## Development setup

```bash
git clone https://github.com/popnow-ai/popnow-go
cd popnow-go
gofmt -l .
go vet ./...
go test -race ./...
```

The tests run against local fake servers and need no API key. Offline, set
`POPNOW_SKIP_LIVE_CONTRACT=1` to skip the comparison with the published OpenAPI spec.

## Guidelines

- Standard library only: no third-party modules.
- Keep `go.mod` at Go 1.23; CI tests 1.23 and 1.25.
- Follow the API contract in the [OpenAPI spec](https://popnow.ai/docs/openapi.yaml); do not add endpoints or
  fields that are not documented. When the spec changes, refresh `contracts/openapi.yaml` and re-check the
  constants `contract_test.go` pins.
- Every Go block in `README.md` is compiled by `readme_test.go`; register new blocks there.
- Write code, comments, docs and commit messages in English.
- Never commit API keys, webhook secrets or real task output links.

## Pull requests

1. Fork the repository and create a branch.
2. Add or update tests and run the three commands above.
3. Describe the change and link any related issue.
