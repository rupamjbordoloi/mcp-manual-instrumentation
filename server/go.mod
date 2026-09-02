module server

go 1.26.4

replace library => ../library

require (
	github.com/modelcontextprotocol/go-sdk v1.7.0
	go.opentelemetry.io/otelc/instrumentation/go.opentelemetry.io/otel/init v0.0.0-00010101000000-000000000000
	go.opentelemetry.io/otelc/instrumentation/go.opentelemetry.io/otel/sdk/trace v0.0.0-00010101000000-000000000000
	go.opentelemetry.io/otelc/instrumentation/go.opentelemetry.io/otel/trace v0.0.0-00010101000000-000000000000
	go.opentelemetry.io/otelc/instrumentation/log v0.0.0-00010101000000-000000000000
	go.opentelemetry.io/otelc/instrumentation/log/slog v0.0.0-00010101000000-000000000000
	go.opentelemetry.io/otelc/instrumentation/net/http/server v0.0.0-00010101000000-000000000000
	go.opentelemetry.io/otelc/instrumentation/runtime v0.0.0-00010101000000-000000000000
	library v0.0.0-00010101000000-000000000000
)

require (
	github.com/bmatcuk/doublestar/v4 v4.10.0 // indirect
	github.com/dave/dst v0.27.4 // indirect
	github.com/gofrs/flock v0.13.0 // indirect
	github.com/urfave/cli/v3 v3.10.1 // indirect
	github.com/valyala/bytebufferpool v1.0.0 // indirect
	github.com/valyala/fasttemplate v1.2.2 // indirect
	golang.org/x/mod v0.37.0 // indirect
	golang.org/x/tools v0.47.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)

require (
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cenkalti/backoff/v5 v5.0.3 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.29.0 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/prometheus/client_golang v1.23.2 // indirect
	github.com/prometheus/client_model v0.6.2 // indirect
	github.com/prometheus/common v0.67.5 // indirect
	github.com/prometheus/otlptranslator v1.0.0 // indirect
	github.com/prometheus/procfs v0.20.1 // indirect
	github.com/segmentio/asm v1.1.3 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/contrib/bridges/prometheus v0.69.0 // indirect
	go.opentelemetry.io/contrib/exporters/autoexport v0.69.0 // indirect
	go.opentelemetry.io/contrib/instrumentation/runtime v0.69.0 // indirect
	go.opentelemetry.io/otel v1.45.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc v0.20.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp v0.20.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc v1.44.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp v1.44.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.45.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.45.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp v1.44.0 // indirect
	go.opentelemetry.io/otel/exporters/prometheus v0.66.0 // indirect
	go.opentelemetry.io/otel/exporters/stdout/stdoutlog v0.20.0 // indirect
	go.opentelemetry.io/otel/exporters/stdout/stdoutmetric v1.44.0 // indirect
	go.opentelemetry.io/otel/exporters/stdout/stdouttrace v1.44.0 // indirect
	go.opentelemetry.io/otel/log v0.20.0 // indirect
	go.opentelemetry.io/otel/metric v1.45.0 // indirect
	go.opentelemetry.io/otel/sdk v1.45.0 // indirect
	go.opentelemetry.io/otel/sdk/log v0.20.0 // indirect
	go.opentelemetry.io/otel/sdk/metric v1.45.0 // indirect
	go.opentelemetry.io/otel/trace v1.45.0 // indirect
	go.opentelemetry.io/otelc v1.0.1 // indirect
	go.opentelemetry.io/otelc/instrumentation v0.0.0-00010101000000-000000000000 // indirect
	go.opentelemetry.io/otelc/pkg v0.0.0 // indirect
	go.opentelemetry.io/otelc/pkg/runtime v0.0.0-20260818033640-f56d9588d11c // indirect
	go.opentelemetry.io/proto/otlp v1.11.0 // indirect
	go.yaml.in/yaml/v2 v2.4.4 // indirect
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/oauth2 v0.36.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.40.0 // indirect
	golang.org/x/time v0.15.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20260803160001-6ac0973c030d // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260803160001-6ac0973c030d // indirect
	google.golang.org/grpc v1.83.0 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

tool go.opentelemetry.io/otelc/tool/cmd/otelc

replace go.opentelemetry.io/otelc/instrumentation/log => D:\rupam\go\auto-instrumentation\manual-mcp-server\server\.otelc-build\instrumentation\log

replace go.opentelemetry.io/otelc/instrumentation/net/http/server => D:\rupam\go\auto-instrumentation\manual-mcp-server\server\.otelc-build\instrumentation\net\http\server

replace go.opentelemetry.io/otelc/instrumentation/go.opentelemetry.io/otel/sdk/trace => D:\rupam\go\auto-instrumentation\manual-mcp-server\server\.otelc-build\instrumentation\go.opentelemetry.io\otel\sdk\trace

replace go.opentelemetry.io/otelc/instrumentation/google.golang.org/grpc/client => D:\rupam\go\auto-instrumentation\manual-mcp-server\server\.otelc-build\instrumentation\google.golang.org\grpc\client

replace go.opentelemetry.io/otelc/instrumentation/go.opentelemetry.io/otel => D:\rupam\go\auto-instrumentation\manual-mcp-server\server\.otelc-build\instrumentation\go.opentelemetry.io\otel

replace go.opentelemetry.io/otelc/pkg => D:\rupam\go\auto-instrumentation\manual-mcp-server\server\.otelc-build\pkg

replace go.opentelemetry.io/otelc/pkg/runtime => D:\rupam\go\auto-instrumentation\manual-mcp-server\server\.otelc-build\pkg\runtime

replace go.opentelemetry.io/otelc/instrumentation => D:\rupam\go\auto-instrumentation\manual-mcp-server\server\.otelc-build\instrumentation

replace go.opentelemetry.io/otelc/instrumentation/net/http/client => D:\rupam\go\auto-instrumentation\manual-mcp-server\server\.otelc-build\instrumentation\net\http\client

replace go.opentelemetry.io/otelc/instrumentation/google.golang.org/grpc/server => D:\rupam\go\auto-instrumentation\manual-mcp-server\server\.otelc-build\instrumentation\google.golang.org\grpc\server

replace go.opentelemetry.io/otelc/instrumentation/go.opentelemetry.io/otel/init => D:\rupam\go\auto-instrumentation\manual-mcp-server\server\.otelc-build\instrumentation\go.opentelemetry.io\otel\init

replace go.opentelemetry.io/otelc/instrumentation/runtime => D:\rupam\go\auto-instrumentation\manual-mcp-server\server\.otelc-build\instrumentation\runtime

replace go.opentelemetry.io/otelc/instrumentation/log/slog => D:\rupam\go\auto-instrumentation\manual-mcp-server\server\.otelc-build\instrumentation\log\slog

replace go.opentelemetry.io/otelc/instrumentation/go.opentelemetry.io/otel/trace => D:\rupam\go\auto-instrumentation\manual-mcp-server\server\.otelc-build\instrumentation\go.opentelemetry.io\otel\trace
