# MCP Go Application -- OpenTelemetry Compile-Time Auto-Instrumentation

> **Audience:** Developers building MCP clients and MCP servers in Go\
> **Owner:** Platform Solution Team\
> **Status:** Developer Integration Guide\
> **Last updated:** 05 October 2026

------------------------------------------------------------------------

## 1. Overview

The Platform Solution Team provides a Go library that adds OpenTelemetry
instrumentation to MCP client and MCP server applications at **compile
time**.

The solution is built on the OpenTelemetry Go compile-time
instrumentation tool, `otelc`. `otelc` wraps the normal Go build process
and injects instrumentation into the application binary during
compilation. This means developers do **not** need to add OpenTelemetry
middleware, tracer initialization, meter initialization, or other
runtime instrumentation code to their MCP application.

The developer integration is intentionally lightweight:

1.  Install `otelc`.
2.  Add the Platform Solution Team instrumentation library to the
    generated instrumentation file.
3.  Build the application using `otelc go build .`.
4.  Configure telemetry through environment variables.
5.  Run the application normally.

OpenTelemetry documents compile-time instrumentation as a build-time
approach that injects instrumentation into the resulting binary rather
than requiring a runtime agent.\
Reference: https://opentelemetry.io/docs/zero-code/go/compile-time/

### What developers need to change

Developers only need to make the following changes to their MCP application:

1. **Install `otelc`**

   ```bash
   go install go.opentelemetry.io/otelc/tool/cmd/otelc@latest
   ```

2. **Add an `otel.instrument.go` file** to the application.

3. **Add the Platform Solution Team instrumentation library** to the file:
   - MCP server: `library/server`
   - MCP client: `library/client`

4. **Build the application using `otelc`:**

   ```bash
   otelc go build .
   ```

5. **Configure OpenTelemetry through environment variables** at runtime, such as the service name, OTLP endpoint, exporters, and sampling configuration.

### Developers do NOT need to change

Developers do **not** need to:

- Add OpenTelemetry middleware manually.
- Add `otel.SetTracerProvider(...)`.
- Add `otel.SetMeterProvider(...)`.
- Add manual OpenTelemetry exporter initialization.
- Modify existing MCP business logic.
- Add tracing code around individual MCP tools.
- Add runtime instrumentation agents.

The compile-time instrumentation provided by `otelc` and the Platform Solution Team library handles these changes during the build process.

------------------------------------------------------------------------

# 2. How the solution works

The instrumentation has two parts:

### 2.1 Platform instrumentation library

The Platform Solution Team provides two instrumentation packages:

-   `library/client` -- for MCP client applications
-   `library/server` -- for MCP server applications

These libraries contain the MCP-specific compile-time instrumentation
hooks.

### 2.2 OpenTelemetry `otelc`

`otelc` uses the Go toolchain's compile-time instrumentation mechanism
to identify supported packages/functions and inject instrumentation
hooks into the compiled application.

The important point is that instrumentation is applied **during
compilation**. The application source code does not need to be modified
to add middleware or OpenTelemetry SDK setup.

------------------------------------------------------------------------

# 3. Prerequisites

Before integrating the instrumentation, make sure the following are
available.

## 3.1 Go

**Go 1.25 or newer** is required for this project.

Verify:

``` bash
go version
```

Expected:

``` text
go version go1.25.x ...
```

## 3.2 Go application

The MCP application must be a Go module.

Verify:

``` bash
go env GOMOD
```

The command should return the path to the application's `go.mod`.

## 3.3 Network access

The first build may need access to the Go module proxy or the
organization's configured Go module repository.

------------------------------------------------------------------------

# 4. Install `otelc`

Install the OpenTelemetry Go compile-time instrumentation tool:

``` bash
go install go.opentelemetry.io/otelc/tool/cmd/otelc@latest
```

Verify that the executable is available:

``` bash
otelc --help
```

If `otelc` is not found, make sure the Go binary directory is present in
`PATH`.

The default Go binary directory is normally:

``` text
$(go env GOPATH)/bin
```

For example:

``` bash
go env GOPATH
```

Then add:

``` text
<gopath>/bin
```

to your `PATH`.

> **Recommendation:** In CI/CD, pin the `otelc` version instead of
> relying on `@latest`, so that application builds remain reproducible.

Example:

``` bash
go install go.opentelemetry.io/otelc/tool/cmd/otelc@<approved-version>
```

------------------------------------------------------------------------

# 5. Add `otel.instrumentation.go`

The only application-side source file required for instrumentation is:

``` text
otel.instrumentation.go
```

The file should be placed in the Go module/package that is being built.

The standard structure is:

``` text
<application>
├── go.mod
├── go.sum
├── otel.instrumentation.go
├── main.go
└── ...
```

------------------------------------------------------------------------

# 6. MCP Server integration

For an MCP server, add the Platform Solution Team server instrumentation
package.

## 6.1 `otel.instrument.go`

``` go
// This file is automatically generated by otelc pin for tracking instrumentations.

// Additional instrumentation imports may be added manually.

//go:build tools

//go:generate go tool otelc pin --generate

package tools

import (
    _ "library/server"

    _ "go.opentelemetry.io/otelc/instrumentation/go.opentelemetry.io/otel/init"
    _ "go.opentelemetry.io/otelc/instrumentation/log"
    _ "go.opentelemetry.io/otelc/instrumentation/log/slog"
    _ "go.opentelemetry.io/otelc/instrumentation/net/http/client"
    _ "go.opentelemetry.io/otelc/instrumentation/net/http/server"
    _ "go.opentelemetry.io/otelc/instrumentation/runtime"
)
```

### Important

The Platform instrumentation import is:

``` go
_ "library/server"
```

The remaining imports enable the required OpenTelemetry instrumentation
components.

Do not remove the Platform instrumentation import.

------------------------------------------------------------------------

# 7. MCP Client integration

For an MCP client, use the Platform Solution Team client instrumentation
package.

## 7.1 `otel.instrument.go`

``` go
// This file is automatically generated by otelc pin for tracking instrumentations.

// Additional instrumentation imports may be added manually.

//go:build tools

//go:generate go tool otelc pin --generate

package tools

import (
    _ "library/client"

    _ "go.opentelemetry.io/otelc/instrumentation/go.opentelemetry.io/otel/init"
    _ "go.opentelemetry.io/otelc/instrumentation/log"
    _ "go.opentelemetry.io/otelc/instrumentation/log/slog"
    _ "go.opentelemetry.io/otelc/instrumentation/net/http/client"
    _ "go.opentelemetry.io/otelc/instrumentation/net/http/server"
    _ "go.opentelemetry.io/otelc/instrumentation/runtime"
)
```

### Important

The Platform instrumentation import is:

``` go
_ "library/client"
```

------------------------------------------------------------------------

# 8. Why `otelc pin` is required

`otelc pin` is used to create/maintain the instrumentation tracking
file.

The application should not rely on manually maintaining a list of
instrumentation imports without running the pin workflow.

The intended workflow is:

``` text
Developer adds/updates instrumentation
             |
             v
       otelc pin
             |
             v
    otel.instrumentation.go
             |
             v
     otelc go build .
             |
             v
    Instrumented binary
```

The `go:generate` directive in the file is:

``` go
//go:generate go tool otelc pin --generate
```

Run:

``` bash
go generate
```

or run the pin command directly when appropriate:

``` bash
go tool otelc pin --generate
```

The generated instrumentation file must retain the Platform Solution
Team import:

``` go
_ "library/client"
```

or:

``` go
_ "library/server"
```

depending on the application.

> **Important:** If the generated file is regenerated, verify that the
> Platform instrumentation import is still present.

------------------------------------------------------------------------

# 9. Build the MCP application

Once `otel.instrumentation.go` has been added, build the application using
`otelc`.

## 9.1 MCP Server

``` bash
otelc go build .
```

## 9.2 MCP Client

``` bash
otelc go build .
```

This replaces the normal:

``` bash
go build .
```

for instrumented builds.

### What happens during the build?

When this command is executed:

``` bash
otelc go build .
```

`otelc` intercepts the Go compilation process and injects the applicable
instrumentation into the generated binary.

The developer's application source remains unchanged.

Conceptually:

``` text
                 otelc go build .
                         |
                         v
              +----------------------+
              | Go compilation       |
              +----------------------+
                         |
             +-----------+-----------+
             |                       |
             v                       v
      Standard Go code       OTel + MCP hooks
             |                       |
             +-----------+-----------+
                         |
                         v
                 Instrumented binary
```

OpenTelemetry describes this as compile-time instrumentation using the
Go toolchain's `-toolexec` mechanism.\
Reference: https://opentelemetry.io/docs/zero-code/go/compile-time/

------------------------------------------------------------------------

# 10. Local development setup

The following example assumes the repository contains an OpenTelemetry
Collector configuration under `infra`.

## 10.1 Start the Collector

``` bash
cd infra
docker compose up -d
```

Verify:

``` bash
docker compose ps
```

View Collector logs:

``` bash
docker compose logs --tail 0 -f otel-collector
```

------------------------------------------------------------------------

# 11. Run the MCP server

The following example uses Windows command syntax.

## Windows

``` bat
cd example\server

otelc go build .

set "OTEL_SERVICE_NAME=mcp-server" && set "OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318" && set "OTEL_LOG_LEVEL=debug" && set "OTEL_METRICS_EXPORTER=otlp" && set "OTEL_GO_DISABLED_INSTRUMENTATIONS=runtimemetrics" && set "OTEL_METRIC_EXPORT_INTERVAL=1000" && server.exe
```

## macOS / Linux

``` bash
cd example/server

otelc go build .

OTEL_SERVICE_NAME=mcp-server \
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318 \
OTEL_LOG_LEVEL=debug \
OTEL_METRICS_EXPORTER=otlp \
OTEL_GO_DISABLED_INSTRUMENTATIONS=runtimemetrics \
OTEL_METRIC_EXPORT_INTERVAL=1000 \
./server
```

> **Placeholder:** Replace the application binary name and working
> directory with the actual MCP server project.

------------------------------------------------------------------------

# 12. Run the MCP client

## Windows

``` bat
cd example\client

otelc go build .

set "OTEL_SERVICE_NAME=mcp-client" && set "OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318" && set "OTEL_LOG_LEVEL=info" && client.exe
```

## macOS / Linux

``` bash
cd example/client

otelc go build .

OTEL_SERVICE_NAME=mcp-client \
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318 \
OTEL_LOG_LEVEL=info \
./client
```

> **Placeholder:** Replace the application binary name and working
> directory with the actual MCP client project.

------------------------------------------------------------------------

# 13. Test the MCP application

Once the server and client are running, use the MCP API to generate
traffic.

## 13.1 Single tool call

### Windows

``` bat
curl -X POST http://localhost:8090/call -H "Content-Type: application/json" -d "{\"tools\": [{\"tool\": \"greet\", \"args\": {\"name\": \"HDFC\"}}]}"
```

### macOS / Linux

``` bash
curl -X POST http://localhost:8090/call \
  -H "Content-Type: application/json" \
  -d '{"tools": [{"tool": "greet", "args": {"name": "HDFC"}}]}'
```

------------------------------------------------------------------------

## 13.2 Another single tool call

### Windows

``` bat
curl -X POST http://localhost:8090/call -H "Content-Type: application/json" -d "{\"tools\": [{\"tool\": \"bye\", \"args\": {\"name\": \"HDFC\"}}]}"
```

### macOS / Linux

``` bash
curl -X POST http://localhost:8090/call \
  -H "Content-Type: application/json" \
  -d '{"tools": [{"tool": "bye", "args": {"name": "HDFC"}}]}'
```

------------------------------------------------------------------------

## 13.3 Multiple tool calls

### Windows

``` bat
curl -X POST http://localhost:8090/call -H "Content-Type: application/json" -d "{\"tools\": [{\"tool\": \"greet\", \"args\": {\"name\": \"HDFC\"}}, {\"tool\": \"bye\", \"args\": {\"name\": \"HDFC\"}}]}"
```

### macOS / Linux

``` bash
curl -X POST http://localhost:8090/call \
  -H "Content-Type: application/json" \
  -d '{"tools": [{"tool": "greet", "args": {"name": "HDFC"}}, {"tool": "bye", "args": {"name": "HDFC"}}]}'
```

------------------------------------------------------------------------

# 14. MCP session and trace behavior

The MCP client creates a session and reuses that session for subsequent
MCP tool calls.

The expected trace model is therefore:

``` text
MCP Client Session
        |
        +---- Tool Call 1
        |
        +---- Tool Call 2
        |
        +---- Tool Call 3
```

The Platform instrumentation is intended to make the MCP interaction
observable at both the session and tool-call levels.

------------------------------------------------------------------------

# 15. Trace examples

The following screenshots should be added to this section.

## 15.1 MCP session trace

**Description:** Grafana trace showing the MCP client session and the
associated server/client spans.

> **\[INSERT IMAGE HERE -- Grafana MCP session trace\]**

**Screenshot:**\
`[Paste session trace screenshot here]`

------------------------------------------------------------------------

## 15.2 Single MCP tool-call trace

**Description:** Grafana trace showing a single MCP tool invocation and
its associated instrumentation.

> **\[INSERT IMAGE HERE -- Grafana single tool-call trace\]**

**Screenshot:**\
`[Paste single tool-call trace screenshot here]`

------------------------------------------------------------------------

## 15.3 Multiple/parallel MCP tool-call trace

**Description:** Grafana trace showing multiple MCP tool calls executed
as part of the same request/session.

> **\[INSERT IMAGE HERE -- Grafana multiple/parallel tool-call trace\]**

**Screenshot:**\
`[Paste multiple tool-call trace screenshot here]`

------------------------------------------------------------------------

# 16. View traces in Grafana

Open:

``` text
http://localhost:3000
```

Then:

1.  Open **Explore**.
2.  Select the configured trace data source.
3.  Search for the service name:
    -   `mcp-server`
    -   `mcp-client`
4.  Open the generated trace.
5.  Validate the MCP session and tool-call spans.

To troubleshoot Collector ingestion:

``` bash
docker compose logs --tail 0 -f otel-collector
```

------------------------------------------------------------------------

# 17. OpenTelemetry environment variables

OpenTelemetry defines standard environment variables for configuring SDK
behavior.

Reference:

https://opentelemetry.io/docs/specs/otel/configuration/sdk-environment-variables/

The table below contains the variables commonly required for this
integration.

## 17.1 Service/resource configuration

  ---------------------------------------------------------------------------------------------------------
  Variable                     Purpose                 Example
  ---------------------------- ----------------------- ----------------------------------------------------
  `OTEL_SERVICE_NAME`          Sets the `service.name` `mcp-server`
                               resource attribute      

  `OTEL_RESOURCE_ATTRIBUTES`   Adds resource           `deployment.environment=dev,service.version=1.0.0`
                               attributes              

  `OTEL_SDK_DISABLED`          Disables the            `true` / `false`
                               OpenTelemetry SDK       
  ---------------------------------------------------------------------------------------------------------

Example:

``` bash
OTEL_SERVICE_NAME=mcp-server
OTEL_RESOURCE_ATTRIBUTES="deployment.environment=dev,service.version=1.0.0"
```

`OTEL_SERVICE_NAME` takes precedence if `service.name` is also supplied
through `OTEL_RESOURCE_ATTRIBUTES`.

------------------------------------------------------------------------

# 18. OTLP exporter configuration

## 18.1 Common endpoint

``` text
OTEL_EXPORTER_OTLP_ENDPOINT
```

Example:

``` text
http://localhost:4318
```

For OTLP/HTTP, signal-specific paths are derived from the base endpoint.

For example:

``` text
http://localhost:4318/v1/traces
http://localhost:4318/v1/metrics
http://localhost:4318/v1/logs
```

Reference:

https://opentelemetry.io/docs/languages/sdk-configuration/otlp-exporter/

------------------------------------------------------------------------

## 18.2 Signal-specific endpoints
  
| Variable | Purpose |
| --- | --- |
| `OTEL_EXPORTER_OTLP_TRACES_ENDPOINT` | Trace endpoint |
| `OTEL_EXPORTER_OTLP_METRICS_ENDPOINT` | Metrics endpoint |
| `OTEL_EXPORTER_OTLP_LOGS_ENDPOINT`  | Logs endpoint |

Example:

``` bash
OTEL_EXPORTER_OTLP_TRACES_ENDPOINT=http://localhost:4318/v1/traces
OTEL_EXPORTER_OTLP_METRICS_ENDPOINT=http://localhost:4318/v1/metrics
OTEL_EXPORTER_OTLP_LOGS_ENDPOINT=http://localhost:4318/v1/logs
```

Signal-specific endpoint variables take precedence over the common
endpoint for that signal.

------------------------------------------------------------------------

# 19. Exporter selection

## Traces

``` text
OTEL_TRACES_EXPORTER
```

Supported standard values include:

| Variable | Purpose |
| --- | --- |
| `otlp` | Export using OTLP |
| `zipkin` | Export using Zipkin |
| `console`  | Write traces to stdout |
| `none`  | Disable automatic trace exporting |


Example:

``` bash
OTEL_TRACES_EXPORTER=otlp
```

------------------------------------------------------------------------

## Metrics

``` text
OTEL_METRICS_EXPORTER
```

Supported standard values include:

| Variable | Purpose |
| --- | --- |
| `otlp` | Export using OTLP |
| `prometheus` | Prometheus exporter |
| `console`  | Write metrics to stdout |
| `none`  | Disable automatic metric exporting |


Example:

``` bash
OTEL_METRICS_EXPORTER=otlp
```

------------------------------------------------------------------------

## Logs

``` text
OTEL_LOGS_EXPORTER
```

Supported standard values include:

| Variable | Purpose |
| --- | --- |
| `otlp` | Export using OTLP |
| `console`  | Write logs to stdout |
| `none`  | Disable automatic log exporting |


Example:

``` bash
OTEL_LOGS_EXPORTER=otlp
```

The OpenTelemetry specification also documents `logging` as a deprecated
compatibility value. New configurations should prefer `console` where
stdout export is required.

Reference:

https://opentelemetry.io/docs/specs/otel/configuration/sdk-environment-variables/


# 20. Metrics export interval

The periodic metric reader uses:

``` text
OTEL_METRIC_EXPORT_INTERVAL
```

The value is specified in milliseconds.

Example:

``` text
OTEL_METRIC_EXPORT_INTERVAL=1000
```

This requests a 1-second interval between the start of metric export
attempts.

The OpenTelemetry specification defines the default as 60,000
milliseconds.

For local testing:

``` text
OTEL_METRIC_EXPORT_INTERVAL=1000
```

For production, use an interval appropriate for the application's
telemetry volume and backend requirements.

Reference:

https://opentelemetry.io/docs/specs/otel/configuration/sdk-environment-variables/

------------------------------------------------------------------------

# 21. Metric export timeout

``` text
OTEL_METRIC_EXPORT_TIMEOUT
```

Example:

``` text
OTEL_METRIC_EXPORT_TIMEOUT=30000
```

The value is in milliseconds.

------------------------------------------------------------------------

# 22. Trace sampling

Trace sampling can be configured using:

``` text
OTEL_TRACES_SAMPLER
```

Common standard values include:

| Value | Description  |
| --- | --- |
| `always_on` | Sample every trace |
| `always_off` | Sample no traces |
| `traceidratio` | Sample based on a trace ID ratio |
| `parentbased_always_on` | Parent-based sampler with always-on root behavior |
| `parentbased_always_off` | Parent-based sampler with always-off root behavior |
| `parentbased_traceidratio` | Parent-based ratio sampler |


For a local development environment:

``` bash
OTEL_TRACES_SAMPLER=always_on
```

The sampling configuration should be reviewed before using high sampling
rates in production.

------------------------------------------------------------------------

# 23. Trace sampling ratio

When using a ratio sampler:

``` text
OTEL_TRACES_SAMPLER_ARG
```

Example:

``` bash
OTEL_TRACES_SAMPLER=traceidratio
OTEL_TRACES_SAMPLER_ARG=0.10
```

This requests approximately 10% sampling.

------------------------------------------------------------------------

# 24. Propagators

OpenTelemetry context propagation can be configured using:

``` text
OTEL_PROPAGATORS
```

Common propagators include:

``` text
tracecontext
baggage
```

Example:

``` bash
OTEL_PROPAGATORS=tracecontext,baggage
```

The exact supported propagators depend on the SDK/instrumentation
implementation.

------------------------------------------------------------------------

# 25. Attribute and span limits

OpenTelemetry also supports limits such as:

``` text
OTEL_ATTRIBUTE_COUNT_LIMIT
OTEL_ATTRIBUTE_VALUE_LENGTH_LIMIT
OTEL_SPAN_ATTRIBUTE_COUNT_LIMIT
OTEL_SPAN_ATTRIBUTE_VALUE_LENGTH_LIMIT
OTEL_SPAN_EVENT_COUNT_LIMIT
OTEL_SPAN_LINK_COUNT_LIMIT
```

These can be useful when controlling telemetry size and cardinality.

Example:

``` bash
OTEL_SPAN_ATTRIBUTE_COUNT_LIMIT=128
```

Refer to the OpenTelemetry environment-variable specification for the
complete set of limits.

------------------------------------------------------------------------

# 26. Compile-time instrumentation selection

The Go compile-time instrumentation implementation supports runtime
enable/disable controls for instrumentation packages.

## Enable instrumentation

``` text
OTEL_GO_ENABLED_INSTRUMENTATIONS
```

Example:

``` bash
OTEL_GO_ENABLED_INSTRUMENTATIONS=nethttp
```

Multiple instrumentation keys can be supplied as a comma-separated list
where supported:

``` bash
OTEL_GO_ENABLED_INSTRUMENTATIONS=nethttp,grpc
```

## Disable instrumentation

``` text
OTEL_GO_DISABLED_INSTRUMENTATIONS
```

Example:

``` bash
OTEL_GO_DISABLED_INSTRUMENTATIONS=runtimemetrics
```

The compile-time hooks are still present in the binary, but the
instrumentation can be gated at runtime.

Reference:

https://github.com/open-telemetry/opentelemetry-go-compile-instrumentation

------------------------------------------------------------------------

# 27. OpenTelemetry log level

For troubleshooting the instrumentation:

``` text
OTEL_LOG_LEVEL
```

Common values:

``` text
debug
info
warn
error
```

Example:

``` bash
OTEL_LOG_LEVEL=debug
```

Recommended usage:

-   `debug` -- local troubleshooting
-   `info` -- normal development/operational visibility
-   `warn` -- reduced logging
-   `error` -- errors only

Avoid leaving verbose debug logging enabled in production unless
required for troubleshooting.

------------------------------------------------------------------------

# 28. Recommended local configuration

A useful local server configuration is:

``` text
OTEL_SERVICE_NAME=mcp-server
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318
OTEL_LOG_LEVEL=debug
OTEL_TRACES_EXPORTER=otlp
OTEL_METRICS_EXPORTER=otlp
OTEL_METRIC_EXPORT_INTERVAL=1000
```

A useful local client configuration is:

``` text
OTEL_SERVICE_NAME=mcp-client
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318
OTEL_LOG_LEVEL=info
OTEL_TRACES_EXPORTER=otlp
OTEL_METRICS_EXPORTER=otlp
```

For production, environment values should be supplied by the deployment
platform rather than hard-coded into the application.

------------------------------------------------------------------------

# 29. Metrics

> **TBD**

The following information should be added to this section:

## 29.1 MCP server metrics

  Metric            Description       Unit       Status
  ----------------- ----------------- ---------- --------
  `[metric-name]`   `[description]`   `[unit]`   TBD
  `[metric-name]`   `[description]`   `[unit]`   TBD
  `[metric-name]`   `[description]`   `[unit]`   TBD

## 29.2 MCP client metrics

  Metric            Description       Unit       Status
  ----------------- ----------------- ---------- --------
  `[metric-name]`   `[description]`   `[unit]`   TBD
  `[metric-name]`   `[description]`   `[unit]`   TBD

## 29.3 Runtime metrics

Examples may include Go runtime/process metrics generated by the enabled
runtime instrumentation.

> Add the final supported metric list here after the Platform Solution
> Team confirms the instrumentation coverage.

## 29.4 HTTP metrics

The HTTP instrumentation may provide HTTP server/client telemetry.

> Add the final metric names and semantic conventions here after
> validation against the approved instrumentation version.

## 29.5 MCP-specific metrics

The Platform instrumentation should document MCP-specific metrics
separately from generic HTTP/runtime metrics.

Potential categories to document include:

-   MCP sessions
-   MCP tool calls
-   MCP tool-call duration
-   MCP tool-call errors
-   MCP request volume
-   MCP client/server operation counts
-   Concurrent MCP operations
-   MCP session duration

> The final metric names, attributes, units, and cardinality should be
> added only after the instrumentation contract is finalized.

------------------------------------------------------------------------

# 30. Application changes that are NOT required

Developers do not need to add code similar to:

``` go
otel.SetTracerProvider(...)
```

or:

``` go
otel.SetMeterProvider(...)
```

or:

``` go
http.Handle("/", otelhttp.NewHandler(...))
```

or manually initialize an OpenTelemetry exporter.

The compile-time instrumentation library and `otelc` build process are
responsible for injecting the required instrumentation.

The application can continue to use its existing MCP implementation.

------------------------------------------------------------------------

# 31. Example integration

Before instrumentation:

``` text
mcp-app/
├── go.mod
├── go.sum
├── main.go
└── ...
```

After instrumentation:

``` text
mcp-app/
├── go.mod
├── go.sum
├── main.go
├── otel.instrumentation.go
└── ...
```

The developer then builds:

``` bash
otelc go build .
```

No additional application middleware is required.

------------------------------------------------------------------------

# 32. CI/CD integration

The same build process should be used in CI/CD.

Example:

``` bash
go install go.opentelemetry.io/otelc/tool/cmd/otelc@<approved-version>

otelc go build .
```

The production runtime environment should provide the OpenTelemetry
configuration.

Example:

``` text
OTEL_SERVICE_NAME=mcp-server
OTEL_EXPORTER_OTLP_ENDPOINT=<collector-endpoint>
OTEL_TRACES_EXPORTER=otlp
OTEL_METRICS_EXPORTER=otlp
OTEL_LOGS_EXPORTER=otlp
```

### CI/CD recommendation

Do not depend on a developer's local global installation of `otelc`.

The CI pipeline should install or otherwise provide the approved `otelc`
version explicitly.

------------------------------------------------------------------------

# 33. Troubleshooting

## 33.1 `otelc` command not found

Check:

``` bash
go env GOPATH
```

Then verify:

``` text
<gopath>/bin/otelc
```

Add the Go binary directory to `PATH`.

------------------------------------------------------------------------

## 33.2 No traces are visible

Check the following:

1.  Application was built with:

``` bash
otelc go build .
```

2.  `OTEL_SERVICE_NAME` is set.
3.  `OTEL_TRACES_EXPORTER=otlp` is configured if required.
4.  `OTEL_EXPORTER_OTLP_ENDPOINT` points to the Collector.
5.  Collector is running.
6.  Collector logs show received/exported telemetry.

Run:

``` bash
docker compose ps
docker compose logs --tail 0 -f otel-collector
```

------------------------------------------------------------------------

## 33.3 No metrics are visible

Check:

``` text
OTEL_METRICS_EXPORTER=otlp
```

and:

``` text
OTEL_EXPORTER_OTLP_ENDPOINT=<collector-endpoint>
```

For local testing, reduce the export interval:

``` text
OTEL_METRIC_EXPORT_INTERVAL=1000
```

Also verify that the required metric instrumentation is enabled and has
not been disabled through:

``` text
OTEL_GO_DISABLED_INSTRUMENTATIONS
```

------------------------------------------------------------------------

## 33.4 Instrumentation library is not being applied

Verify that `otel.instrument.go` contains the correct Platform package:

For server:

``` go
_ "library/server"
```

For client:

``` go
_ "library/client"
```

Then rebuild:

``` bash
otelc go build .
```

Do not use only:

``` bash
go build .
```

when testing compile-time instrumentation.

------------------------------------------------------------------------

## 33.5 Instrumentation disappears after regeneration

If `otel.instrumentation.go` is regenerated using `otelc pin`, verify that
the Platform instrumentation import is still present.

Required:

``` go
_ "library/server"
```

or:

``` go
_ "library/client"
```

If the generated file is managed by `go generate`, make sure the
Platform library is included in the supported pin/generation workflow.

------------------------------------------------------------------------

## 33.6 Build takes significantly longer

Compile-time instrumentation performs additional work during the build
because `otelc` intercepts compilation and applies instrumentation
rules.

Compare:

``` bash
go build .
```

with:

``` bash
otelc go build .
```

A longer build time is expected, particularly on the first instrumented
build or after dependency/cache changes.

------------------------------------------------------------------------

# 34. Developer integration checklist

Use this checklist when onboarding an MCP application.

### Prerequisites

-   [ ] Go 1.25 or newer installed
-   [ ] MCP application builds normally
-   [ ] Application is a Go module
-   [ ] Access to the approved Platform instrumentation library

### Installation

-   [ ] Install approved `otelc` version
-   [ ] Verify `otelc` is available on `PATH`

### Application setup

-   [ ] Add `otel.instrument.go`
-   [ ] Add `library/server` for MCP server
-   [ ] Add `library/client` for MCP client
-   [ ] Keep required OpenTelemetry instrumentation imports
-   [ ] Run `go generate` / approved `otelc pin` workflow where
    applicable

### Build

-   [ ] Build using `otelc go build .`
-   [ ] Do not use only `go build .` for the instrumented binary

### Runtime

-   [ ] Configure `OTEL_SERVICE_NAME`
-   [ ] Configure OTLP endpoint
-   [ ] Configure required signal exporters
-   [ ] Start the OpenTelemetry Collector
-   [ ] Start MCP server
-   [ ] Start MCP client
-   [ ] Execute MCP tool calls

### Validation

-   [ ] Verify Collector logs
-   [ ] Verify client traces
-   [ ] Verify server traces
-   [ ] Verify MCP session trace
-   [ ] Verify single tool-call trace
-   [ ] Verify multiple/parallel tool-call trace
-   [ ] Verify metrics once the Platform metrics section is enabled

------------------------------------------------------------------------

# 35. Quick-start summary

For an MCP server:

``` bash
# 1. Install otelc
go install go.opentelemetry.io/otelc/tool/cmd/otelc@latest

# 2. Add otel.instrumentation.go
#    Import:
#    _ "library/server"

# 3. Build with compile-time instrumentation
otelc go build .

# 4. Run with OpenTelemetry configuration
OTEL_SERVICE_NAME=mcp-server \
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318 \
OTEL_TRACES_EXPORTER=otlp \
OTEL_METRICS_EXPORTER=otlp \
./server
```

For an MCP client:

``` bash
# 1. Install otelc
go install go.opentelemetry.io/otelc/tool/cmd/otelc@latest

# 2. Add otel.instrument.go
#    Import:
#    _ "library/client"

# 3. Build with compile-time instrumentation
otelc go build .

# 4. Run with OpenTelemetry configuration
OTEL_SERVICE_NAME=mcp-client \
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318 \
OTEL_TRACES_EXPORTER=otlp \
OTEL_METRICS_EXPORTER=otlp \
./client
```

------------------------------------------------------------------------

# 36. References

1.  OpenTelemetry Go compile-time instrumentation\
    https://opentelemetry.io/docs/zero-code/go/compile-time/

2.  OpenTelemetry SDK environment variables\
    https://opentelemetry.io/docs/specs/otel/configuration/sdk-environment-variables/

3.  OpenTelemetry OTLP exporter configuration\
    https://opentelemetry.io/docs/languages/sdk-configuration/otlp-exporter/

4.  OpenTelemetry Protocol exporter specification\
    https://opentelemetry.io/docs/specs/otel/protocol/exporter/

5.  OpenTelemetry Go compile-time instrumentation repository\
    https://github.com/open-telemetry/opentelemetry-go-compile-instrumentation

6.  OpenTelemetry Go instrumentation guide\
    https://github.com/open-telemetry/opentelemetry-go-compile-instrumentation/blob/main/docs/instrument-guide.md

------------------------------------------------------------------------

# 37. Platform Solution Team -- Maintainer Information

|  |  |
| --- | --- |
|  Instrumentation owner           | Platform Solution Team |
|  MCP server package              | `library/server` |
|  MCP client package              | `library/client` |
|  Approved `otelc` version        | `[TBD]` |
|  Approved OpenTelemetry version  | `[TBD]` |
|  Collector endpoint              | `[TBD]` |
|  Grafana URL                     | `[TBD]` |
|  Support channel                 | `[TBD]` |
|  Repository                      | `[TBD]` |
