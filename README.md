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
docker compose up
```

## 2. Build normally through otelc — no --rules flag needed

```bash
go mod tidy
go run .
```

## 3. Run with net/http disabled in otelc, so only otelhttp instruments it

```bash
# terminal A
set "OTEL_SERVICE_NAME=http-server-demo" && set "OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318" && set "OTEL_GO_DISABLED_INSTRUMENTATIONS=nethttp" && server.exe

# terminal B
set "OTEL_SERVICE_NAME=http-client-demo" && set "OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318" && set "OTEL_GO_DISABLED_INSTRUMENTATIONS=nethttp" && client.exe
```

> **Check the exact module name.** `nethttp` is the name implied by the
> project's internal package path
> (`.../pkg/instrumentation/nethttp`), but confirm it against your build:
> run `otelc go build --debug ./server` (or check `.otel-build/`) and look
> for whatever identifier it logs for the net/http rule, then use that
> exact string in `OTEL_GO_DISABLED_INSTRUMENTATIONS`. If it's wrong,
> you'll see duplicate/doubled spans per request (one from otelc, one from
> otelhttp) rather than missing metrics — that's the tell.

## 4. Verify

```bash
curl localhost:8080/hello
curl localhost:8080/other
```

Within ~5s, `http.server.request.duration` (and friends, per OTel HTTP
semantic conventions) should show up once per route in the collector's
console — not twice, and not zero.

## Applying this to Gin instead of net/http

Same shape, different one-liner:

```go
import "go.opentelemetry.io/contrib/instrumentation/github.com/gin-gonic/gin/otelgin"

router := gin.Default()
router.Use(otelgin.Middleware("http-server-demo"))
```

`otelgin` is a well-maintained contrib package with correct metrics
support, so the same "disable otelc's rule for this one library, let the
purpose-built contrib middleware cover it in one line" pattern applies
directly - just swap `OTEL_GO_DISABLED_INSTRUMENTATIONS` to whatever
identifier otelc uses for its Gin (or underlying net/http) rule in your
build's debug output.

## When to drop this entirely

Once [issue #728](https://github.com/open-telemetry/opentelemetry-go-compile-instrumentation/issues/728)
is fixed upstream, remove the `otelhttp`/`otelgin` wrap and the
`OTEL_GO_DISABLED_INSTRUMENTATIONS` env var, and go back to plain
`otelc go build ./server` with zero application code for net/http, same
as the other libraries already get today.
