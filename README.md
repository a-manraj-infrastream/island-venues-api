# island-venues-api

The booking API behind **Island Venues**, a demo app for booking event venues
across Mauritius, from Port Louis to Blue Bay. It was built for the DevFest 2026
talk *"The Era of Agentic Developer Platforms"* and is deployed by
[Infrastream](https://infrastream.io).

It is a small Go service with a venue catalogue, customer bookings, a staff
back office and a signed payment webhook. It publishes a `booking-created`
event that `island-venues-notifier` turns into a confirmation e-mail. Logs,
traces and profiles are wired for Google Cloud from the first commit.

## API

JSON over HTTP/1.1. Every error is `{"error": "<message>"}` with a matching
status code.

| Method & path | Who (enforced at the edge) | Behaviour |
|---|---|---|
| `GET /healthz` | probe | `200 ok` |
| `GET /api/catalog/venues` | public | list venues |
| `GET /api/catalog/venues/{id}` | public | one venue, or 404 |
| `POST /api/bookings` | signed-in | `{venueId, date, guests, contactEmail}` → 201 `PENDING` booking, publishes `booking-created` |
| `GET /api/bookings` | signed-in | the caller's bookings |
| `DELETE /api/bookings/{id}` | signed-in | cancel your own booking (403 otherwise) → `CANCELLED` |
| `GET /api/bookings/sign-in` | signed-in (edge only) | `303` to `/`. The web app's **Sign in** target: the app is served on an open path, so it navigates here to make the edge sign the visitor in, then comes back. Redirects to a fixed path only. |
| `GET /api/admin/bookings[?status=]` | staff | all bookings, optionally filtered by status |
| `POST /api/admin/bookings/{id}/approve` | staff | → `APPROVED` |
| `POST /api/admin/bookings/{id}/reject` | staff | → `REJECTED` |
| `POST /api/admin/venues` | staff | create a venue (id derived from town + name) |
| `POST /webhooks/payments` | public, HMAC-verified | `{bookingId, status:"PAID"}` → `PAID` |

**Booking rules.** `date` is `YYYY-MM-DD`, a real calendar day, not in the past
(judged in Mauritius time, UTC+4) and at most 3 years ahead. `guests` is
between 1 and the venue's capacity. `contactEmail` is a bare address. A venue
holds one live booking per date. `CANCELLED` and `REJECTED` bookings free the
date. PostgreSQL enforces this with a partial unique index, so two requests
racing for the same date cannot both win.

**Status machine.**

| From | Approve | Reject | Owner cancels | Webhook pays |
|---|---|---|---|---|
| PENDING | APPROVED | REJECTED | CANCELLED | PAID |
| APPROVED | 409 | 409 | CANCELLED | PAID |
| PAID | 409 | 409 | 409 | 200 (idempotent) |
| REJECTED / CANCELLED | 409 | 409 | 409 | 409 |

**Payment webhook.** The provider signs the raw body with HMAC-SHA256 using
`PAYMENT_WEBHOOK_SECRET` and sends the hex digest in `X-Signature`. The service
returns 401 for a missing or wrong signature and 503 if no secret is
configured. It checks the signature before it parses the body.

## Caller identity

The load balancer authenticates users. The service verifies them again, so a
request that skips the edge cannot claim to be someone else.

- **`IAP_AUDIENCE` set.** The `X-Goog-IAP-JWT-Assertion` header is checked with
  `idtoken.Validate`. The issuer must be `https://cloud.google.com/iap` and
  the audience must be on the accepted list. The caller is the `gcip.email`
  claim (Identity Platform) or else the `email` claim. Anything else gets 401.
- **Else, `DEV_MODE=true`.** The caller comes from the `X-Dev-User` header. This
  is for local development only. The service refuses to start in this mode on
  Cloud Run (`K_SERVICE` set) unless IAP is configured.
- **Else.** Signed-in and staff routes return 401.

`X-Goog-Authenticated-User-Email` is never trusted. IAP enforces membership of
the `venue-ops` staff group at the edge. If the staff routes sit behind their
own IAP backend service, set `IAP_STAFF_AUDIENCE` to that backend's audience.
Staff routes then accept only assertions minted for it, which proves in code
that the request passed the staff-only IAP policy.

## Run locally

Requires Go 1.26 or newer.

```sh
# In-memory store, events logged instead of published, dev identity.
DEV_MODE=true PAYMENT_WEBHOOK_SECRET=local-secret go run main.go

curl localhost:8080/api/catalog/venues
curl -X POST localhost:8080/api/bookings -H 'X-Dev-User: alice@example.mu' \
  -d '{"venueId":"grand-baie-lagoon-deck","date":"2026-12-24","guests":80,"contactEmail":"alice@example.mu"}'

# Simulate the payment provider.
BODY='{"bookingId":"<id>","status":"PAID"}'
SIG=$(printf '%s' "$BODY" | openssl dgst -sha256 -hmac local-secret -hex | awk '{print $NF}')
curl -X POST localhost:8080/webhooks/payments -H "X-Signature: $SIG" -d "$BODY"
```

To use PostgreSQL, set `DATABASE_URL`, for example
`postgres://user:pass@localhost:5432/venues?sslmode=disable`. Migrations are
embedded and run at every start-up. They are idempotent and serialised with
an advisory lock, and the seed uses `ON CONFLICT DO NOTHING`.

### Tests

```sh
go vet ./...
go test ./...
# Optional: run the store contract tests against a disposable PostgreSQL too.
ISLAND_VENUES_TEST_DATABASE_URL=postgres://... go test ./internal/store/
```

The handler tests run over the in-memory store. The same store contract suite
runs against both implementations, so they cannot drift apart. No test needs
GCP: Pub/Sub is exercised against the in-process `pstest` fake.

## Environment variables

| Variable | Default | Purpose |
|---|---|---|
| `PORT` | `8080` | HTTP listen port (Cloud Run sets it). |
| `DATABASE_URL` | *(unset → in-memory)* | PostgreSQL / AlloyDB DSN (pgx format). |
| `IAP_AUDIENCE` | *(unset)* | Accepted IAP audience(s), comma-separated: `/projects/<number>/global/backendServices/<id>`. |
| `IAP_STAFF_AUDIENCE` | *(unset → `IAP_AUDIENCE`)* | Optional audience(s) accepted on `/api/admin/*` only. Requires `IAP_AUDIENCE`. |
| `DEV_MODE` | `false` | Trust `X-Dev-User`. Local only; ignored when `IAP_AUDIENCE` is set. |
| `PUBSUB_TOPIC` | *(unset → log only)* | Topic id for `booking-created`. Needs `GOOGLE_CLOUD_PROJECT`. |
| `GOOGLE_CLOUD_PROJECT` | *(unset)* | Enables Pub/Sub, the Cloud Trace exporter, Cloud Profiler and fully qualified trace ids in logs. |
| `PAYMENT_WEBHOOK_SECRET` | *(unset → webhook 503)* | HMAC-SHA256 key for `X-Signature`. Store it in Secret Manager. A trailing newline (as left by `echo ... \| gcloud secrets versions add`) is ignored. |
| `SERVICE_NAME` | `island-venues-api` | Profiler service name and trace resource. |
| `SERVICE_VERSION` | `RELEASE_VERSION`, else `INFRASTREAM_APP_VERSION`, else `dev` | Profiler version and trace resource. The engine-built image sets `RELEASE_VERSION` and the engine sets `INFRASTREAM_APP_VERSION`. |
| `LOG_LEVEL` | `INFO` | `DEBUG`, `INFO`, `WARN` or `ERROR` (case-insensitive); an unknown value means `INFO`. |
| `K_SERVICE` | *(set by Cloud Run)* | Used only to refuse `DEV_MODE` on Cloud Run. |

## Events

`booking-created` carries a JSON payload:

```json
{"bookingId":"…","venueId":"…","venueName":"…","date":"2026-12-24","guests":80,
 "owner":"alice@example.mu","contactEmail":"alice@example.mu","createdAt":"2026-10-03T10:00:00Z"}
```

Message attributes include `eventType=booking-created` and the W3C
`traceparent`, so the notifier can continue the same trace.

## Observability

- **Logs.** `log/slog` writes JSON to stdout using Cloud Logging's field names
  (`severity`, `message`, `time`). While a request is being served, each line
  also carries `logging.googleapis.com/trace`
  (`projects/<project>/traces/<id>`), `…/spanId` and `…/trace_sampled`, so
  application logs nest under their request in Logs Explorer and Cloud Trace.
  There is one access-log line per request in the `httpRequest` format.
- **Traces.** The OpenTelemetry SDK puts an `otelhttp` server span on every
  route, named after the route pattern. Incoming W3C `traceparent` and
  Google `X-Cloud-Trace-Context` headers are both honoured. Spans export to
  Cloud Trace when `GOOGLE_CLOUD_PROJECT` is set. Pub/Sub publishes are child
  spans.
- **Profiles.** Cloud Profiler starts when `GOOGLE_CLOUD_PROJECT` is set.
- **Shutdown.** On SIGTERM the server drains for up to 6 s, then closes the
  publisher and flushes spans, all inside Cloud Run's 10 s grace period.

## Deployment with Infrastream

This repository is application code only: there is no Dockerfile and no CI
workflow. Infrastream generates both. The manifests live in
[`a-manraj-infrastream/infrastream-organization-manifests-ab204170`](https://github.com/a-manraj-infrastream/infrastream-organization-manifests-ab204170):

- A `BuildDefinition` of type `GOLANG` (target `main.go`, `buildType: bin`)
  makes the engine stamp a GitHub Actions pipeline. The pipeline runs
  golangci-lint, govulncheck and `go test ./...`, then `go build main.go`
  (static, `CGO_ENABLED=0`). It packages the binary into a `scratch` image and
  pushes it to the organisation's private Artifact Registry.
- An `Application` (`target: CLOUD_RUN`, `meshStrategy: SIDECAR`) deploys it
  to Cloud Run in `us-central1` inside the `island-venues` tenant project. The
  engine also wires the load-balancer routes, their IAP protection, the
  Pub/Sub topic, the AlloyDB database and the environment above.

## Known gaps

- **Event publishing is not transactional.** If Pub/Sub fails after the
  booking is stored, the API still returns 201 and logs the failure at ERROR
  with the booking id. A transactional outbox would close this gap.
- **The Cloud Trace exporter and propagator are deprecated upstream.**
  `opentelemetry-operations-go/exporter/trace` will be archived after
  2027-01-01. Migrating to OTLP (`telemetry.googleapis.com`) is a drop-in
  change confined to `internal/telemetry`.
- **Cancelling someone else's booking returns 403, not 404.** This is what the
  contract specifies, and it reveals that the booking id exists. Ids are 128-bit
  random values, so they cannot be enumerated.

## License

Apache-2.0. See [LICENSE](LICENSE).
