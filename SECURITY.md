# Security policy

## Reporting a vulnerability

Please do **not** open a public issue for security problems.

- Open a [private security advisory](https://github.com/popnow-ai/popnow-go/security/advisories/new) on GitHub, or
- email **support@popnow.ai** with "Security" in the subject.

Include the affected version, a description, and steps to reproduce. Never include a live API key; if you think
a key was exposed, revoke it in the [API console](https://popnow.ai/api/console/keys) first.

## Supported versions

Security fixes are released for the latest minor version of `github.com/popnow-ai/popnow-go`.

## Handling secrets

- Keep `POPNOW_API_KEY` and `POPNOW_WEBHOOK_SECRET` on your server, in environment variables or a secret store.
- Do not log the `Authorization` header. This client never sends it to object storage and redacts presigned
  upload URLs from its errors.
- Output URLs (`Output.Assets[].URL`) are short-lived bearer links; do not publish or store them.
