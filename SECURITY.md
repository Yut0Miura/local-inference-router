# Security

## Threat Model

The router sits between applications and inference runtimes on infrastructure the operator controls. The assets are the backends' credentials, the content of prompts and answers, and the reachability of the backends themselves.

In scope: a client trying to reach a backend the configuration does not allow, credential leakage in either direction, unbounded request bodies, accidental network exposure, and secrets or prompts appearing in logs.

Out of scope: a hostile operator, a compromised backend that returns malicious content, multi-tenant isolation and per-user quotas.

## Trusted Configuration

The YAML file is trusted operator input, not an untrusted policy language. It decides every upstream destination. Validation exists to catch operator mistakes early: unknown fields, a second document, an oversized file, malformed URLs, out-of-range capacities and timeouts, duplicate names and unresolved backend references are all refused.

## Inbound Authentication

Optional, enabled with `bearer_token_env`. When set, the chat, models, status and metrics endpoints require `Authorization: Bearer <token>`; liveness and readiness stay open because they reveal no topology.

The token is compared with `crypto/subtle` in constant time. It is accepted only from the header — never from a query string, cookie or request body. A failure returns `401` with `WWW-Authenticate: Bearer` and no hint about which part was wrong.

## Backend Authentication

Each backend may have its own `bearer_token_env`. That token is attached to inference and health requests for that backend only. `serve` refuses to start if a configured variable is missing or empty, so a silent unauthenticated fallback is impossible.

## Credential Isolation

A client's `Authorization` header is never copied upstream, and a backend token never reaches a client. Client `Cookie` headers are dropped, and `Set-Cookie` from a backend is not forwarded downstream. Tokens are never logged and never appear in metric labels or in `/router/status`.

## Request Size Bounds

Chat bodies are limited to 4 MiB with `http.MaxBytesReader`; larger bodies get `413`. Compressed request bodies are rejected with `415` rather than decoded. Request headers are limited to 1 MiB. The configuration file is limited to 1 MiB.

## Static Upstream Paths

Upstream URLs are built with `net/url` from a validated origin plus one fixed path, `/v1/chat/completions`, or the fixed health path of the backend kind. There is no string concatenation and no request-controlled component, so a client cannot steer a request to another path, host or service.

## Redirect Protection

Both the inference and health clients refuse to follow redirects. A `3xx` is returned to the client as the answer and is not failover-eligible, so a backend cannot redirect the router into an unvetted destination.

## Backend URL Trust

`base_url` must be an origin: `http` or `https`, a host, and nothing else. A path, query, fragment or embedded credentials are rejected. This keeps the upstream surface exactly as wide as the operator wrote it.

## Prompt & Response Privacy

Prompts, messages, request bodies and response bodies are never logged. Streaming responses are relayed byte for byte and are never inspected or stored. Logs carry alias names, backend names, statuses, durations and error categories — enough to operate the router, not to reconstruct traffic.

## Network Exposure

The default listen address is `127.0.0.1:8080`. Listening on `0.0.0.0` or any other interface has to be written in the configuration, so exposure is always a deliberate act. The compose file publishes the router on `127.0.0.1` only and does not publish Ollama at all.

## TLS

The router serves plain HTTP and is expected to run behind a reverse proxy or on a trusted network when exposed. Backends may be reached over HTTPS: `base_url` accepts `https`, and the TLS handshake timeout is 5 seconds. There is no option to skip certificate verification.

## Known Limitations

- No inbound TLS listener in this version.
- One shared inbound token, with no per-client identity, rotation or quotas.
- No rate limiting: capacity control is `max_inflight` per backend, which protects the backends, not the router.
- A healthy backend is not proof that a configured model exists.
- Health and in-flight state live in memory: two router processes do not share capacity.

## Vulnerability Reporting

Please report security issues privately through GitHub's **Report a vulnerability** option on this repository instead of opening a public issue. Include the affected file and steps to reproduce.
