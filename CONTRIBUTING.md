# Contributing

## Requirements

- Go 1.27.x
- Docker with Buildx for the container image
- A C toolchain for `go test -race`

## Before Opening a Pull Request

```bash
make check
```

This runs gofmt, `go vet`, the tests, the race tests, `govulncheck` and the build.

## Rules

- Standard library first. The only runtime dependencies are `go.yaml.in/yaml/v3` and the Prometheus client; do not add web frameworks, CLI frameworks, logging or HTTP helper libraries.
- Keep the configuration the single source of every upstream destination. Nothing a client sends may select a backend, a host or a path.
- Never log prompts, bodies or credentials, and never put unbounded values into metric labels.
- Failover stays bounded: nothing may retry after the response has been committed.
- New behaviour needs a test that fails without it, and concurrency changes need a race test.
- No new runtime dependency without justification, and no scope expansion without an issue first.

## Commits

Use short conventional prefixes such as `feat:`, `fix:`, `test:`, `ci:`, `security:`, `docs:` and `chore:`.
