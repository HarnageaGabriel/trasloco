# ADR 0007: HTTP credentials, redirects, retries and rate limits

Status: Accepted — 2026-10-06. Source: architecture review of M0 + M1 wave 1.

## Context
Permission 403s were retried for minutes; the limiter wrapped the whole retry loop; credentials were chosen by host only; auth could follow redirects; every response body was buffered for the cache.

## Decision
- Credentials are selected by `(host, target scope)` and carry a verified `Actor` (resolved at preflight). One credential per actor+host+scope (GitHub ToS).
- A single `ratelimit` package owns budgets and pacing state; `credpool` only selects; `httpx` only applies.
- The limiter is acquired per attempt inside the retry loop; retry waits are `max(Retry-After, backoff with full jitter, blockedUntil)`.
- Retry only on network errors, 408/429/5xx, and 403 when it is a rate-limit signal (Retry-After present, remaining = 0, or a secondary-limit body). Never on plain permission errors. POST/PATCH only with an explicit idempotency guard.
- GitHub: proactive pacing below 10% remaining; secondary limit budget of 900 points per minute (REST) and GraphQL point costs; content-creation 80/min and 500/h. ADO: adjust the existing pacer from headers on every response, including HTTP 200 with Retry-After.
- Redirects: credentials are never sent to a different origin; pre-signed URLs are fetched anonymously.
- Caching is opt-in per request, only for responses with validators (ETag or Last-Modified) and below a size cap; everything else is streamed. Cache failures are logged, never fatal.

## Consequences
Correct behaviour at fleet scale and no credential leaks across origins.
