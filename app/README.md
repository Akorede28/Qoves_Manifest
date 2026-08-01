# app/, the QOVES take-home API

A Go implementation of the contract the brief (Take-home assignment) specifies. The brief permits a
rewrite in another language provided the endpoints and the `DATABASE_URL`
contract are preserved, and states the application itself is not scored; the
platform around it is the assignment. **ADR-6** in `docs/WRITEUP.md` explains
why I took that option rather than deploying the provided Flask app.

## The contract

| Endpoint       | Behaviour                                                              |
| -------------- | ---------------------------------------------------------------------- |
| `GET /`        | Returns a hello string. No dependencies, this is the liveness path     |
| `GET /healthz` | Runs `SELECT 1` against Postgres: **200** if reachable, **503** if not |
| `GET /metrics` | Prometheus exposition format                                           |

The connection string is read from the `DATABASE_URL` environment variable and
is not hard-coded anywhere. The process refuses to start if the variable is
unset, rather than falling back to a default; a silent fallback would defeat
the point of Part E, which is that the value arrives from a Secret at runtime.
It is delivered via `secretKeyRef` from `app-db-credentials`, which is
materialised in-cluster from the SealedSecret ciphertext in
`manifests/secrets/`.

## Metrics

Three series, one of which is beyond the brief:

- `http_requests_total{code,path}`, counter, as specified.
- `http_request_duration_seconds{path}`, histogram. Not required, but it is the
  input the scaling signal in ADR-4 would actually use, and the argument that
  CPU is the wrong HPA signal is worth more when the right one is already
  being exported.
- `app_database_up`, gauge, `1` if the last database health check succeeded
  and `0` otherwise. **This is the one deliberate addition to the contract.**
  Without it the only available database alert is inferred from a counter of
  `/healthz` 503s, which alerts on a proxy for the condition rather than on the
  condition. `ApiDatabaseUnreachable` in `manifests/monitoring/` is built on
  this gauge; see ADR-7 and the runbook.

## Build

`.github/workflows/build-image.yml` builds on push and pushes to GHCR, tagged by
commit SHA, printing the digest. `manifests/api/api.yaml` pins that digest, not
a tag, so the deployed image is immutable and survives tag mutation or
deletion. Changing the app is therefore a two-step flow: push code, then commit
the new digest. That is deliberate; it keeps the cluster's source of truth a git
value rather than whatever a registry currently points a tag at.

The runtime image is distroless `:nonroot`. There is no shell in it, which is
why the NetworkPolicy demonstration in `docs/evidence/` uses a separate busybox
pod and why the proof that the _allowed_ network path works is `/healthz`
returning 200 through the ingress.
