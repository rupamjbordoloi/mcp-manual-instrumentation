## 1. Start the collector

```bash
cd infra
docker compose up -d
```

## 2. Install otelc

```bash
go install go.opentelemetry.io/otelc/tool/cmd/otelc@latest
```
This places the otelc binary in your Go bin directory ($(go env GOPATH)/bin by default). The following steps assume otelc is on your PATH. If not then please add it to the path

## 2. Build normally through otelc — no --rules flag needed

```bash

# terminal A
cd example/server
otelc go build .
set "OTEL_SERVICE_NAME=mcp-server" && set "OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318" && set "OTEL_LOG_LEVEL=debug" && set "OTEL_METRICS_EXPORTER=none" && set "OTEL_GO_DISABLED_INSTRUMENTATIONS=runtimemetrics" && server.exe

# terminal B
cd example/client
otelc go build .
set "OTEL_SERVICE_NAME=mcp-client" && set "OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318" && set "OTEL_LOG_LEVEL=info" && client.exe
```

## 3. View traces
- go to http://localhost:3000
- visit Explore
- check otel logs using  `docker compose logs --tail 0 -f otel-collector`

## workflow
- `otelc pin` command will generate `otelc.instrumentation.go` file in both client and server depending on the packages used that needs to be instrumented
- need to manually add our custom `library` into the generated `otelc.instrumentation.go`
- when `otelc.instrumentation.go` is added to import our custom library, then otelc will not automatically instrument the application, thats why need to use the `otelc pin`.

```bash
curl -X POST localhost:8090/call -d "{\"tool\":\"greet\",\"args\":{\"name\":\"HDFC\"}}"
```

```bash
curl -X POST http://localhost:8090/call \
  -H "Content-Type: application/json" \
  -d '{"tools": [{"tool": "greet", "args": {"name": "HDFC"}}, {"tool": "bye", "args": {"name": "HDFC"}}]}'

curl -X POST http://localhost:8090/call -H "Content-Type: application/json" -d "{\"tools\": [{\"tool\": \"greet\", \"args\": {\"name\": \"HDFC\"}}, {\"tool\": \"bye\", \"args\": {\"name\": \"HDFC\"}}]}"


curl -X POST http://localhost:8090/call \
  -H "Content-Type: application/json" \
  -d '{"tools": [{"tool": "greet", "args": {"name": "HDFC"}}]}'

curl -X POST http://localhost:8090/call -H "Content-Type: application/json" -d "{\"tools\": [{\"tool\": \"bye\", \"args\": {\"name\": \"HDFC\"}}]}"
```