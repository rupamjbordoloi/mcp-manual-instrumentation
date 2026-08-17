# Single workaround: otelc for everything, otelhttp for net/http metrics

Applies once, at the router/client level — no per-handler rules, no custom
hooks. Works for `net/http`, and the same idea (one middleware, applied
once) works for Gin (`otelgin.Middleware(...)` via `router.Use(...)`) or
any other framework with an official `contrib` instrumentation package.

## What's disabled vs. what stays automatic

| Concern                              | Handled by            |
|---------------------------------------|------------------------|
| net/http traces + metrics             | `otelhttp` (1 line)   |
| database/sql traces + metrics         | `otelc` (automatic)   |
| gRPC traces + metrics                 | `otelc` (automatic)   |
| Redis traces + metrics                | `otelc` (automatic)   |
| Go runtime metrics                    | `otelc` (automatic)   |

Only `otelc`'s own net/http rule is turned off, because it's the one with
the metrics gap. Everything else `otelc` supports keeps working with zero
code, same as before.

## 1. Start the collector

```bash
docker compose up -d
```

## 2. Build normally through otelc — no --rules flag needed

```bash
# go mod tidy

# terminal A
cd server
otelc go build .
set "OTEL_GO_DISABLED_INSTRUMENTATIONS=nethttp" && server.exe

# terminal B
cd client
otelc go build .
set "OTEL_GO_DISABLED_INSTRUMENTATIONS=nethttp" && client.exe
```

## 3. View traces
- go to http://localhost:3000
- visit Explore