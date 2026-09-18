# Architecture

## Purpose

Route OpenAI-compatible chat-completion requests across already-running local or self-hosted LLM inference backends, choosing a backend that is healthy and below its capacity, and streaming the answer back unchanged.

## System Boundary

The router owns request routing and nothing else. Model download, loading, unloading and scheduling belong to the backends. There is no database, no cache, no queue and no cluster state: two router processes share nothing.

Everything a request can reach comes from the configuration file. A client can pick an alias and nothing else: not a backend, not a host, not an upstream path.

## Components

| Package | Responsibility |
|---------|----------------|
| `internal/config` | loading, defaults and validation of the YAML file, and base URL rules |
| `internal/backend` | backend definitions, HTTP clients and health checking |
| `internal/router` | shared routing state, selection, capacity reservation, proxying and streaming |
| `internal/httpapi` | endpoints, request validation, inbound authentication |
| `internal/metrics` | the Prometheus registry and instruments |
| `internal/cli` | the `serve` and `validate` commands |

`cmd/local-inference-router` only calls `cli.Run` and exits with its code.

## Configuration

One YAML document, `version: 1`, decoded with unknown fields rejected. A file larger than 1 MiB is refused before decoding, and an empty file or a second document is an error.

Validation covers listen address, backend kinds, base URL shape, capacity and timeout bounds, environment variable names, alias and route references, priorities and duplicates. It never touches the network, so `validate` is safe to run anywhere.

Defaults: `listen` becomes `127.0.0.1:8080`, `response_header_timeout_ms` becomes `120000`.

## Backend Health

Each backend is polled on its own goroutine: once immediately at startup, then every 5 seconds, with a 2-second timeout per check. The path is fixed by kind (`/api/version` for Ollama, `/health` for llama.cpp and vLLM). Only `200` is healthy; the body is never parsed.

Health checks use a separate HTTP client and never consume `max_inflight`. If a backend has a token configured, the health request carries it too.

A backend starts as "not checked", which is treated as not healthy. Health transitions are logged by backend name.

## Routing State

One mutex protects health flags, in-flight counters and reservations. The state also answers readiness and `/router/status` from the same data, so those views cannot disagree with routing decisions.

Aliases and backends keep configuration order, which makes status output and tie-breaking deterministic.

## Capacity Reservation

Selection and the increment of the chosen backend's in-flight counter happen under one lock. Two concurrent requests therefore cannot both see the last free slot.

The reservation is released exactly once, whatever ends the attempt: a completed response, a transport failure, a failover, client cancellation or a copy error. Counters never go negative.

A streaming response holds its reservation until the stream finishes, because the backend is still working until then.

## Request Flow

```text
POST /v1/chat/completions
  → inbound authentication (when configured)
  → media type, encoding and 4 MiB body limit
  → JSON object with a non-empty "model"
  → alias lookup
  → reserve a route
  → upstream request with the alias replaced by upstream_model
  → response commitment
  → relay body (flushed per write when it is an event stream)
  → release reservation
```

## Bounded Failover

The router may try another route only while nothing has been written downstream. After `WriteHeader` the response is committed and the attempt is final: a later read error is logged, never retried.

Retryable before commitment: transport, DNS, TLS and response-header-timeout failures, and the statuses `429`, `502`, `503`, `504`. Not retryable: client cancellation and every other status, including `3xx`, `401`, `404` and `500`.

Each route is tried at most once, there is no backoff, and the attempt count is bounded by the number of routes in the alias. On a retryable status the next route is reserved *before* the current reservation is released, so the freed slot cannot be taken by another request in between; only then is the abandoned response body closed.

## Streaming

A response whose media type is `text/event-stream` is relayed through a writer that flushes after every write, with a reusable 32 KiB buffer. Events are neither parsed nor rewritten, and the body is never accumulated in memory. Non-streaming responses use the same buffered copy without flushing.

If the downstream writer cannot flush and the upstream is a stream, the router closes the upstream and returns `500` — this is only possible before commitment, so the client gets a clean error instead of a stalled stream.

## Authentication Boundary

Inbound authentication is optional and covers the chat, models, status and metrics endpoints. Liveness and readiness are always open.

Backend credentials are separate configuration and are the only credentials ever sent upstream. Client `Authorization` and `Cookie` headers stop at the router.

## Metrics

A dedicated registry holds seven instruments: inbound request count and duration, upstream attempt count and duration, per-backend in-flight and health gauges, and a failover counter. Labels are fixed vocabularies — route names, backend names, alias names, attempt results and failover reasons — so cardinality cannot grow with traffic.

## Concurrency

- One mutex for all routing state; no lock is held while an upstream request is in flight.
- One goroutine per backend for health checks, all stopped by the server context, so shutdown leaks nothing.
- The reservation and release pair is the only shared mutable counter on the request path.
- The concurrency tests run hundreds of goroutines against a small capacity under `-race`, and assert that the observed number of simultaneous reservations never exceeds the configured capacity.

## Non-Goals

Model lifecycle, model discovery, response rewriting, queueing, backoff, per-user quotas, service discovery, custom health paths, a web UI, persistence and distributed state are all out of scope for this version.
