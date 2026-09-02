# Contributing

Bareplane is early-stage. Keep changes small, testable, and tied to an issue.

## Development

```bash
go test ./...
go vet ./...
go build ./...
```

Run `make check` before opening a pull request.

## Pull requests

- Use one branch per issue.
- Keep infrastructure-specific logic behind provider boundaries.
- Add tests for new behavior and regression fixes.
- Avoid generated files unless they are part of a documented user-facing contract.
- Prefer explicit errors over silent fallback behavior.
