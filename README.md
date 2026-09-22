# Local LLM Inference Router

**Go router for local LLM inference across Ollama, llama.cpp and vLLM, with health-aware routing, streaming and bounded failover.**

## Why

Once more than one local inference runtime is running, every application has to answer the same questions: which backend is up, which one is busy, what happens when one of them fails mid-request, and how a model name maps to whatever each runtime calls it.

This router answers them in one small process. Applications keep sending an ordinary OpenAI-compatible chat request to one address; the router picks a healthy backend that still has capacity, rewrites the model name and streams the answer straight back.

## Architecture

```text
Application
    │
    │ POST /v1/chat/completions
    ▼
Local LLM Inference Router
    │
    ├── alias resolution
    ├── health-aware routing
    ├── max-inflight control
    ├── streaming passthrough
    └── bounded pre-response failover
          │
          ├──────── Ollama
          ├──────── llama.cpp
          └──────── vLLM
```

The router is stateless apart from health and in-flight counters held in memory. It does not start, stop, load or download models.

## What This Is / Is Not

| It is | It is not |
|-------|-----------|
| a routing layer in front of running local inference runtimes | a model server or model manager |
| health- and capacity-aware selection between configured routes | an AI gateway platform |
| a streaming reverse proxy for chat completions | a full OpenAI-compatible API surface |
| bounded failover before a response starts | a retry framework with queueing and backoff |

The router does not manage model lifecycle. Models are pulled, loaded and unloaded by the backends themselves.

## Supported Backends

| Kind | Health endpoint |
|------|-----------------|
| `ollama` | `GET /api/version` |
| `llama_cpp` | `GET /health` |
| `vllm` | `GET /health` |

Every backend must already be running and must expose an OpenAI-compatible `POST /v1/chat/completions` endpoint.

## Features

- OpenAI-compatible `POST /v1/chat/completions` in front of several local runtimes
- model aliases with priority-ordered routes
- health checks per backend, on a fixed path per kind
- per-backend `max_inflight` capacity, shared by every alias that uses the backend
- server-sent event streaming, relayed without buffering or rewriting
- bounded failover: another route may be tried only before the response starts
- optional inbound bearer authentication, constant-time compared
- Prometheus metrics on a dedicated registry
- one static binary, distroless container image, no external services

## Quick Start

Prerequisites

- Go 1.27.x when building the router locally
- at least one supported inference backend already running: Ollama, llama.cpp or vLLM
- the model referenced by a configured route already available in that backend

The router does not start inference runtimes or download, load or unload models.
make build

```bash
make build
./bin/local-inference-router validate -config examples/config.yaml
./bin/local-inference-router serve -config examples/config.yaml
```

With Ollama running locally and `qwen3:0.6b` pulled, the example configuration is ready to use:

```bash
curl http://127.0.0.1:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "local-chat",
    "messages": [
      {
        "role": "user",
        "content": "Reply with one short sentence."
      }
    ]
  }'
```

## Configuration

```yaml
version: 1

listen: "127.0.0.1:8080"

bearer_token_env: ""

backends:
  - name: "ollama-local"
    kind: "ollama"
    base_url: "http://127.0.0.1:11434"
    max_inflight: 4
    response_header_timeout_ms: 120000
    bearer_token_env: ""

aliases:
  - name: "local-chat"
    routes:
      - backend: "ollama-local"
        upstream_model: "qwen3:0.6b"
        priority: 10
```

| Field | Meaning |
|-------|---------|
| `version` | must be `1` |
| `listen` | `host:port`. Empty means `127.0.0.1:8080`; exposure beyond loopback must be written out explicitly |
| `bearer_token_env` | name of the environment variable holding the inbound token. Empty disables inbound authentication |
| `backends[].kind` | `ollama`, `llama_cpp` or `vllm`, case-sensitive |
| `backends[].base_url` | an origin only: scheme and host, no path, query, fragment or credentials |
| `backends[].max_inflight` | `1`–`100000` concurrent inference requests for this backend |
| `backends[].response_header_timeout_ms` | `1000`–`900000`, default `120000`. Applies until upstream headers arrive, not to the stream that follows |
| `backends[].bearer_token_env` | environment variable holding this backend's own token |
| `aliases[].routes[].priority` | `0`–`1000000`; a smaller value is preferred |

Tokens are never written in the file, only the names of environment variables. There is no `bearer_token` field.

`validate` checks names and structure without touching the network or requiring the variables to exist. `serve` requires every configured variable to be set and non-empty, and fails to start otherwise.

Unknown fields, a second YAML document, an empty file and a file larger than 1 MiB are rejected.

## Model Aliases

An alias is a public model name that maps to one or more routes:

```yaml
aliases:
  - name: "local-chat"
    routes:
      - backend: "ollama-local"
        upstream_model: "qwen3:0.6b"
        priority: 10
      - backend: "vllm-a"
        upstream_model: "Qwen/Qwen3-8B"
        priority: 20
```

The client sends `"model": "local-chat"`. The router replaces it with the route's `upstream_model` and leaves every other field of the request as it was, including fields it does not know.

Alias matching is exact and case-sensitive. An unknown alias returns `404`.

`GET /v1/models` lists configured aliases. It is router configuration, not discovery: the router never queries a backend's model list, never checks that a model exists and never changes routes based on what a backend reports.

## Routing

For each attempt the router:

1. skips routes already tried for this request;
2. keeps routes whose backend is checked and healthy;
3. keeps routes whose backend is below `max_inflight`;
4. prefers the lowest priority value;
5. within one priority, prefers the backend with the fewest in-flight requests;
6. on an exact tie, keeps configuration order.

Selection and the reservation that follows happen under one lock, so concurrent requests cannot push a backend past its capacity.

`max_inflight` belongs to the backend. If several aliases point at the same backend, they share it. A streaming request stays in flight until the stream ends.

There is no queue: if nothing is both healthy and below capacity, the router answers `503` immediately.

## Bounded Failover

**Failover is allowed only before the downstream response has been committed.** Once the first bytes of a backend's answer have been written to the client, switching backends could only produce a corrupted response, so the attempt is final.

Before that point the router tries the next route when:

- the upstream connection, DNS, TLS or response-header timeout fails, or
- the upstream answers `429`, `502`, `503` or `504`.

It does **not** fail over when:

- the client cancels the request;
- the upstream answers any other status, including `400`, `401`, `403`, `404`, `408`, `409`, `422`, `500`, `501`, `505` and any `3xx`. These usually mean a configuration problem, and silently moving traffic elsewhere would hide it.

Each route is attempted at most once per request, there is no delay or backoff between attempts, and the number of attempts is bounded by the number of routes configured for the alias.

If a retryable status arrives and no other route is available, that upstream response is forwarded as it is, not replaced with a router error. If the final attempt fails at the transport level, the router answers `504` (`upstream_timeout`) for a timeout and `502` (`upstream_unavailable`) otherwise.

## Streaming

When the upstream answers with `text/event-stream`, the router relays bytes incrementally and flushes after every write. It does not buffer the response, parse events or rewrite their JSON.

The response `model` field is **not** rewritten: a streamed answer carries whatever model name the backend reports. Rewriting it would mean parsing and re-encoding every event of every backend's dialect, which this version deliberately does not do.

## API

| Endpoint | Purpose |
|----------|---------|
| `POST /v1/chat/completions` | routed chat completion |
| `GET /v1/models` | configured aliases |
| `GET /healthz` | process liveness |
| `GET /readyz` | routing readiness |
| `GET /router/status` | current backend and alias state |
| `GET /metrics` | Prometheus metrics |

Anything else returns `404`. `/v1/completions`, `/v1/responses`, `/v1/embeddings`, `/v1/images` and `/v1/audio` are not implemented.

Chat requests must be `application/json`, must not be compressed and must be at most 4 MiB. The body must be a JSON object with a non-empty `model` string; the rest of the OpenAI schema is not validated.

## Health & Readiness

Each backend is checked on its own schedule: immediately at startup, then every 5 seconds, with a 2-second timeout. Only HTTP `200` counts as healthy; the body is ignored. Health checks do not consume `max_inflight`.

`GET /healthz` reports process liveness and returns `ok`.

`GET /readyz` returns `ready` only when every configured alias has at least one route whose backend has been checked and is healthy; otherwise `503 not ready`. A healthy backend at full capacity still counts as ready.

**Readiness verifies healthy configured backend routes, not whether an upstream model is loaded or available.** A backend can be healthy while the configured `upstream_model` is missing; that error comes back from the backend on a real request.

`GET /router/status` returns the current state and deliberately exposes no base URLs, model names, tokens or environment values:

```json
{
  "backends": [
    {
      "name": "ollama-local",
      "kind": "ollama",
      "checked": true,
      "healthy": true,
      "inflight": 1,
      "max_inflight": 4
    }
  ],
  "aliases": [
    {
      "name": "local-chat",
      "ready": true,
      "route_count": 1
    }
  ]
}
```

## Authentication

Inbound authentication is optional and enabled by setting `bearer_token_env`:

```yaml
bearer_token_env: "ROUTER_TOKEN"
```

```bash
ROUTER_TOKEN=... ./bin/local-inference-router serve -config config.yaml
curl -H "Authorization: Bearer $ROUTER_TOKEN" http://127.0.0.1:8080/v1/models
```

When enabled, `POST /v1/chat/completions`, `GET /v1/models`, `GET /router/status` and `GET /metrics` require the token. `GET /healthz` and `GET /readyz` never do: they expose no topology.

The token is accepted only from the `Authorization: Bearer` header, never from a query string, cookie or body, and is compared in constant time. A failure returns `401` with `WWW-Authenticate: Bearer` and does not say whether the header was missing, malformed or wrong.

A client's `Authorization` header is never forwarded to a backend. Backend credentials are configured separately, per backend.

## Metrics

`GET /metrics` serves a dedicated Prometheus registry:

| Metric | Labels |
|--------|--------|
| `local_inference_router_http_requests_total` | `route`, `status` |
| `local_inference_router_http_request_duration_seconds` | `route` |
| `local_inference_router_backend_attempts_total` | `backend`, `alias`, `result` |
| `local_inference_router_backend_attempt_duration_seconds` | `backend`, `alias` |
| `local_inference_router_backend_inflight` | `backend` |
| `local_inference_router_backend_healthy` | `backend` |
| `local_inference_router_failovers_total` | `alias`, `reason` |

`route` is one of `chat_completions`, `models`, `healthz`, `readyz`, `router_status` — never a raw request path. Prompts, request identifiers, upstream URLs, tokens and arbitrary error text never become labels. `/metrics` does not instrument itself.

## Docker

```bash
docker compose up -d ollama
docker compose exec ollama ollama pull qwen3:0.6b
docker compose up --build -d router
```

```bash
curl http://127.0.0.1:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "local-chat",
    "messages": [
      {
        "role": "user",
        "content": "Reply with one short sentence."
      }
    ]
  }'
```

The demo model is small and runs on CPU: no GPU, CUDA or ROCm is required, and no cloud API key. The image is built from `golang:1.27.1-bookworm` into `gcr.io/distroless/static-debian13:nonroot`, runs as UID `65532` and contains no shell or package manager.

## Security Model

- The configuration file is trusted operator input. Everything a request can reach is decided there: a client cannot choose a backend URL, override a host or change the upstream path.
- Upstream URLs are built through `net/url` from a validated origin plus one static path, never by string concatenation.
- `Authorization` and `Cookie` from clients are never forwarded upstream, and the router neither forwards nor adds `Forwarded` or `X-Forwarded-*` headers.
- Redirects are not followed: a `3xx` is returned to the client as the answer.
- Bearer tokens are read from environment variables, never from the YAML file, and are never logged.
- Prompts, messages, request bodies and response bodies are never logged.
- The default listen address is `127.0.0.1`. Exposing the router on a network interface requires writing that address in the configuration.

See [SECURITY.md](SECURITY.md).

## Limitations

- Chat Completions only
- No model discovery
- No model lifecycle
- No response model rewriting
- No request queue
- No post-commit failover
- No per-user quotas
- No dynamic service discovery
- No custom backend health paths
- No Web UI
- No distributed state

"Local" here means self-hosted inference runtimes the operator already runs, whether on the same machine or elsewhere on their own network. It does not mean the router must run on the same host as the models.

## Development

```bash
make check             # gofmt, vet, tests, race tests, govulncheck, build
make validate-example  # validate examples/config.yaml
make docker-build      # build the container image
```

Requires Go 1.27.x. The race detector needs cgo and a C toolchain.

## Testing

Tests cover configuration validation, route selection under concurrency, capacity reservation, health checks per backend kind, readiness, request validation, model rewriting, failover and non-failover statuses, transport failover, client cancellation, streaming, the absence of post-commit failover, authentication, credential isolation, redirects and metrics.

All of them use `httptest`: no Ollama, llama.cpp, vLLM, GPU, cloud model or API key is required, locally or in CI.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md).

## License

Apache License 2.0. See [LICENSE](LICENSE).
