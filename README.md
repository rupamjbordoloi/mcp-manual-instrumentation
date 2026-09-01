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
set "OTEL_SERVICE_NAME=mcp-server" && set "OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318" && set "OTEL_LOG_LEVEL=debug" && server.exe

# terminal B
cd client
otelc go build .
set "OTEL_SERVICE_NAME=mcp-client" && set "OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318" && set "OTEL_LOG_LEVEL=debug" && client.exe
```

## 3. View traces
- go to http://localhost:3000
- visit Explore

## workflow
- `otelc pin` command will generate `otelc.instrumentation.go` file in both client and server depending on the packages used that needs to be instrumented
- need to manually add our custom `library` into the generated `otelc.instrumentation.go`
- when `otelc.instrumentation.go` is added to import our custom library, then otelc will not automatically instrument the application, thats why need to use the `otelc pin`.
