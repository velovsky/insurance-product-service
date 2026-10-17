# insurance-product-service

A small Go + gRPC service for **versioned insurance products and quotes**, built around one question:

> How can people **and AI agents** change insurance tariffs quickly, without ever breaking correctness?

The answer here is the same one you would use for code: changes are proposals, every proposal gets a dry run and a diff, a second pair of eyes approves it, and nothing that was published is ever edited.

## What it does

| Capability | How |
|---|---|
| **Versioned tariffs** | A product has numbered versions. Each has an `effective_from` date and moves `DRAFT → APPROVED → PUBLISHED`. Published versions are immutable. |
| **Point-in-time pricing** | `CreateQuote` prices a risk with the version *in force on a date* and returns the version number, so every quote can be reproduced later. |
| **Explainable premiums** | Base premium × rating factors, minimum premium, premium tax. Each step is a line in the quote. Money is integer cents; multipliers are basis points. No floats. |
| **Dry run for proposals** | `ValidateDraft` returns validation issues, a diff against the version it would replace and the price change for sample risks. It never changes state. |
| **Four-eyes approval** | Whoever proposed a version (`alice` or `agent:pricing-bot`) cannot approve it. |
| **Guardrails** | Multipliers outside 0.1x–5x, past effective dates and timeline overlaps are rejected at approval and re-checked at publish. |
| **Audit outbox** | Every state change writes an event in the same critical section, so the log cannot miss or invent a change. `ListEvents` pages through it. |
| **Machine-readable errors** | Validation failures return `InvalidArgument` with `google.rpc.BadRequest` field violations, so a client or agent can fix a proposal in one pass. |

## Try it

Requires Go 1.27+ and [`buf`](https://buf.build/docs/installation).

```bash
make run          # gRPC on :50051 with a published demo product "travel-basic"
```

Price a trip (the server has reflection, so `buf curl` or `grpcurl` need no proto files):

```bash
buf curl --protocol grpc --http2-prior-knowledge \
  -d '{"product_code":"travel-basic","risk":{"attributes":{"age_band":"65+","destination":"world","trip_length":"month"}}}' \
  http://localhost:50051/insurance.product.v1.QuoteService/CreateQuote
```

```text
Base premium                     EUR  49.00
age_band = 65+ (x1.6)            EUR  29.40
destination = world (x1.45)      EUR  35.28
trip_length = month (x1.8)       EUR  90.94
Insurance premium tax (19%)      EUR  38.88
Total (version 1)                EUR 243.50
```

Let an agent propose a price change, then review it:

```bash
# 1. The agent drafts version 2 (world cover x1.45 -> x1.6) from next year
buf curl --protocol grpc --http2-prior-knowledge -H 'x-actor: agent:pricing-bot' \
  -d @examples/draft-v2.json \
  http://localhost:50051/insurance.product.v1.ProductService/CreateDraftVersion

# 2. Dry run: issues, diff and the impact on a sample customer
buf curl --protocol grpc --http2-prior-knowledge \
  -d '{"product_code":"travel-basic","version_number":2,"sample_risks":[{"attributes":{"age_band":"65+","destination":"world","trip_length":"month"}}]}' \
  http://localhost:50051/insurance.product.v1.ProductService/ValidateDraft
#    -> changes: factors.destination.world x1.45 -> x1.6
#    -> impact:  EUR 243.50 -> EUR 268.69 (+10.34%)

# 3. The agent tries to approve its own proposal
buf curl --protocol grpc --http2-prior-knowledge -H 'x-actor: agent:pricing-bot' \
  -d '{"product_code":"travel-basic","version_number":2}' \
  http://localhost:50051/insurance.product.v1.ProductService/ApproveVersion
#    -> permission_denied: agent:pricing-bot created version 2 and cannot approve it

# 4. A person approves and publishes
buf curl --protocol grpc --http2-prior-knowledge -H 'x-actor: alice' \
  -d '{"product_code":"travel-basic","version_number":2}' \
  http://localhost:50051/insurance.product.v1.ProductService/ApproveVersion
buf curl --protocol grpc --http2-prior-knowledge -H 'x-actor: alice' \
  -d '{"product_code":"travel-basic","version_number":2}' \
  http://localhost:50051/insurance.product.v1.ProductService/PublishVersion
```

Quotes dated before 2027-01-01 keep using version 1; later ones use version 2.

## Design

```
proto/                 API (buf lint STANDARD), source of truth
gen/                   generated Go code, checked in and verified in CI
internal/domain        pure business rules: tariffs, pricing, versions, dry run, diff
internal/store         in-memory repository + audit outbox
internal/service       gRPC transport: requests, actor, error mapping
cmd/server             wiring, logging interceptor, health, reflection, graceful shutdown
```

- **The domain has no transport or storage imports.** Every rule is unit-tested without gRPC; the service tests then run the full lifecycle over a real gRPC connection (`bufconn`).
- **Copy-on-write updates.** `store.Update` mutates a deep copy and swaps it in only on success, so a rejected change can never leave a half-modified product or a stray event.
- **Proposals are stored even when invalid.** An agent's proposal should be inspectable and fixable, not dropped. Validity is enforced at approval and re-checked at publish, because time passes between the two.
- **One pending change per product** keeps the timeline linear and the diff meaningful.
- **The actor comes from `x-actor` metadata.** In production it would come from a verified token; the domain only needs a stable identity to enforce four-eyes.

## Next steps

- PostgreSQL repository with a transactional outbox and a relay to a message broker
- Server-streaming `WatchEvents` for consumers
- Authentication (JWT / mTLS) and per-role permissions (`author`, `approver`, `publisher`)
- An MCP server on top of the API, so agents propose tariffs through typed tools
- OpenTelemetry tracing

## Development

```bash
make test       # go test -race ./...
make lint       # buf lint + format, gofmt, go vet
make generate   # regenerate gen/ after editing proto/
make docker     # distroless image, ~21 MB
```

## License

MIT
