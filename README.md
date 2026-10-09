# Portfolio dashboard

A small, self-contained Go web app for viewing a local portfolio snapshot. The sample values in `portfolio.json` are fictional demo data; replace them with your own local snapshot before use.

## Quick start

Requires Go 1.23 or newer.

```sh
go run .
# then open http://localhost:8080
```

Useful options:

```sh
go run . -addr 127.0.0.1:9090 -data ./portfolio.json
go test ./...
go vet ./...
```

The server has no runtime dependency on a CDN, database, or external service. HTML, CSS, and JavaScript are embedded into the Go binary.

## Endpoints

- `GET /` — responsive dashboard UI.
- `GET /api/portfolio` — normalized snapshot, per-holding calculations, summary, and category allocation as JSON.
- `GET /healthz` — returns `healthy` with HTTP 200.

The file is read and validated at startup. Restart the process after editing the snapshot.

## Snapshot format

`portfolio.json` accepts this shape. Monetary values are in the three-letter `currency` code, and quantities/prices can be decimal numbers.

```json
{
  "as_of": "2026-10-01",
  "currency": "SGD",
  "holdings": [
    {
      "symbol": "NOVA",
      "name": "Nova Foods Co.",
      "quantity": 120,
      "average_cost": 18.40,
      "current_price": 22.15,
      "category": "Equities"
    }
  ]
}
```

Every holding requires a symbol, name, category, non-negative finite quantity, average cost, and current price. `as_of` and a three-letter currency code are required. The server calculates `market_value`, `cost_basis`, `gain_loss`, `gain_loss_pct`, `allocation_pct`, portfolio summary totals, and category allocations; calculated fields do not need to be included in the input.

## Architecture

- `main` loads and validates the JSON snapshot, constructs the HTTP handler, and starts a standard-library `http.Server` with conservative timeouts.
- `portfolio.go` owns validation, normalization, and deterministic calculations. Zero denominators produce a `0` percentage rather than `NaN` or an error.
- `server.go` exposes the JSON and health routes and serves embedded assets.
- `web/` contains semantic HTML, local CSS, and a small fetch/render client. The client visibly reports API failures and includes a mobile layout.

## v1 limitations

- Values are a manually maintained point-in-time snapshot; there is no brokerage connection or live market data.
- There is no authentication, multi-user support, database, trade execution, or persistence beyond the JSON file.
- The app does not provide investment, tax, or financial advice.
- Input values use floating-point arithmetic suitable for a personal overview, not accounting-grade ledger settlement.
- The snapshot is loaded only at startup and all API responses represent that same immutable process snapshot.
