module client

go 1.26.4

replace github.hdfcbank.com/HDFCBANK/ps-otel/instrumentation => ../../../../instrumentation

tool go.opentelemetry.io/otelc/tool/cmd/otelc

require (
	github.com/modelcontextprotocol/go-sdk v1.8.0
	github.hdfcbank.com/HDFCBANK/ps-otel/instrumentation v0.0.0-00010101000000-000000000000
	go.opentelemetry.io/otelc/instrumentation/go.opentelemetry.io/otel/init v0.0.0-20261021153517-5dd2074874c9
	go.opentelemetry.io/otelc/instrumentation/log v0.0.0-20261021153517-5dd2074874c9
	go.opentelemetry.io/otelc/instrumentation/log/slog v0.0.0-20261021153517-5dd2074874c9
	go.opentelemetry.io/otelc/instrumentation/net/http/client v0.0.0-20261021153517-5dd2074874c9
	go.opentelemetry.io/otelc/instrumentation/net/http/server v0.0.0-20261021153517-5dd2074874c9
	go.opentelemetry.io/otelc/instrumentation/runtime v0.0.0-20261021153517-5dd2074874c9
)

require (
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/bmatcuk/doublestar/v4 v4.10.0 // indirect
	github.com/cenkalti/backoff/v5 v5.0.3 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dave/dst v0.28.0 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/gofrs/flock v0.13.1 // indirect
	github.com/google/jsonschema-go v0.4.3 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.32.0 // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/prometheus/client_golang v1.25.0 // indirect
	github.com/prometheus/client_model v0.6.3 // indirect
	github.com/prometheus/common v0.72.1 // indirect
	github.com/prometheus/otlptranslator v1.0.0 // indirect
	github.com/prometheus/procfs v0.22.0 // indirect
	github.com/segmentio/asm v1.2.1 // indirect
	github.com/segmentio/encoding v0.5.4 // indirect
	github.com/urfave/cli/v3 v3.12.0 // indirect
	github.com/valyala/bytebufferpool v1.0.0 // indirect
	github.com/valyala/fasttemplate v1.2.2 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/contrib/bridges/prometheus v0.72.0 // indirect
	go.opentelemetry.io/contrib/exporters/autoexport v0.72.0 // indirect
	go.opentelemetry.io/contrib/instrumentation/runtime v0.72.0 // indirect
	go.opentelemetry.io/contrib/propagators/autoprop v0.72.0 // indirect
	go.opentelemetry.io/contrib/propagators/aws v1.47.0 // indirect
	go.opentelemetry.io/contrib/propagators/b3 v1.47.0 // indirect
	go.opentelemetry.io/contrib/propagators/jaeger v1.47.0 // indirect
	go.opentelemetry.io/contrib/propagators/ot v1.47.0 // indirect
	go.opentelemetry.io/otel v1.47.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc v0.23.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploghttp v0.23.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc v1.47.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp v1.47.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.47.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.47.0 // indirect
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp v1.47.0 // indirect
	go.opentelemetry.io/otel/exporters/prometheus v0.69.0 // indirect
	go.opentelemetry.io/otel/exporters/stdout/stdoutlog v0.23.0 // indirect
	go.opentelemetry.io/otel/exporters/stdout/stdoutmetric v1.47.0 // indirect
	go.opentelemetry.io/otel/exporters/stdout/stdouttrace v1.47.0 // indirect
	go.opentelemetry.io/otel/log v1.47.0 // indirect
	go.opentelemetry.io/otel/metric v1.47.0 // indirect
	go.opentelemetry.io/otel/sdk v1.47.0 // indirect
	go.opentelemetry.io/otel/sdk/log v1.47.0 // indirect
	go.opentelemetry.io/otel/sdk/metric v1.47.0 // indirect
	go.opentelemetry.io/otel/trace v1.47.0 // indirect
	go.opentelemetry.io/otelc v1.2.0 // indirect
	go.opentelemetry.io/otelc/instrumentation v0.0.0-20261021153517-5dd2074874c9 // indirect
	go.opentelemetry.io/otelc/pkg v0.0.0-20261021153517-5dd2074874c9 // indirect
	go.opentelemetry.io/otelc/pkg/runtime v0.0.0-20261021153517-5dd2074874c9 // indirect
	go.opentelemetry.io/proto/otlp v1.11.1 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/mod v0.42.0 // indirect
	golang.org/x/net v0.61.0 // indirect
	golang.org/x/oauth2 v0.37.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/text v0.43.0 // indirect
	golang.org/x/time v0.16.0 // indirect
	golang.org/x/tools v0.52.0 // indirect
	google.golang.org/genproto/googleapis/api v0.0.0-20261019180522-ce908d29299b // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20261019180522-ce908d29299b // indirect
	google.golang.org/grpc v1.84.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)

replace go.opentelemetry.io/otelc/instrumentation => D:\rupam\go\auto-instrumentation\manual-mcp-server\demo\app\mcp\client\.otelc-build\instrumentation

replace go.opentelemetry.io/otelc/instrumentation/database/sql => D:\rupam\go\auto-instrumentation\manual-mcp-server\demo\app\mcp\client\.otelc-build\instrumentation\database\sql

replace go.opentelemetry.io/otelc/instrumentation/github.com/anthropics/anthropic-sdk-go => D:\rupam\go\auto-instrumentation\manual-mcp-server\demo\app\mcp\client\.otelc-build\instrumentation\github.com\anthropics\anthropic-sdk-go

replace go.opentelemetry.io/otelc/instrumentation/github.com/aws/aws-sdk-go-v2 => D:\rupam\go\auto-instrumentation\manual-mcp-server\demo\app\mcp\client\.otelc-build\instrumentation\github.com\aws\aws-sdk-go-v2

replace go.opentelemetry.io/otelc/instrumentation/github.com/gin-gonic/gin => D:\rupam\go\auto-instrumentation\manual-mcp-server\demo\app\mcp\client\.otelc-build\instrumentation\github.com\gin-gonic\gin

replace go.opentelemetry.io/otelc/instrumentation/github.com/linode/linodego/v2 => D:\rupam\go\auto-instrumentation\manual-mcp-server\demo\app\mcp\client\.otelc-build\instrumentation\github.com\linode\linodego\v2

replace go.opentelemetry.io/otelc/instrumentation/github.com/openai/openai-go => D:\rupam\go\auto-instrumentation\manual-mcp-server\demo\app\mcp\client\.otelc-build\instrumentation\github.com\openai\openai-go

replace go.opentelemetry.io/otelc/instrumentation/github.com/openai/openai-go/internal/streaming => D:\rupam\go\auto-instrumentation\manual-mcp-server\demo\app\mcp\client\.otelc-build\instrumentation\github.com\openai\openai-go\internal\streaming

replace go.opentelemetry.io/otelc/instrumentation/github.com/openai/openai-go/v2 => D:\rupam\go\auto-instrumentation\manual-mcp-server\demo\app\mcp\client\.otelc-build\instrumentation\github.com\openai\openai-go\v2

replace go.opentelemetry.io/otelc/instrumentation/github.com/openai/openai-go/v3 => D:\rupam\go\auto-instrumentation\manual-mcp-server\demo\app\mcp\client\.otelc-build\instrumentation\github.com\openai\openai-go\v3

replace go.opentelemetry.io/otelc/instrumentation/github.com/redis/go-redis/v9 => D:\rupam\go\auto-instrumentation\manual-mcp-server\demo\app\mcp\client\.otelc-build\instrumentation\github.com\redis\go-redis\v9

replace go.opentelemetry.io/otelc/instrumentation/github.com/segmentio/kafka-go/consumer => D:\rupam\go\auto-instrumentation\manual-mcp-server\demo\app\mcp\client\.otelc-build\instrumentation\github.com\segmentio\kafka-go\consumer

replace go.opentelemetry.io/otelc/instrumentation/github.com/segmentio/kafka-go/producer => D:\rupam\go\auto-instrumentation\manual-mcp-server\demo\app\mcp\client\.otelc-build\instrumentation\github.com\segmentio\kafka-go\producer

replace go.opentelemetry.io/otelc/instrumentation/github.com/sirupsen/logrus => D:\rupam\go\auto-instrumentation\manual-mcp-server\demo\app\mcp\client\.otelc-build\instrumentation\github.com\sirupsen\logrus

replace go.opentelemetry.io/otelc/instrumentation/go.mongodb.org/mongo-driver/mongo => D:\rupam\go\auto-instrumentation\manual-mcp-server\demo\app\mcp\client\.otelc-build\instrumentation\go.mongodb.org\mongo-driver\mongo

replace go.opentelemetry.io/otelc/instrumentation/go.mongodb.org/mongo-driver/v2/mongo => D:\rupam\go\auto-instrumentation\manual-mcp-server\demo\app\mcp\client\.otelc-build\instrumentation\go.mongodb.org\mongo-driver\v2\mongo

replace go.opentelemetry.io/otelc/instrumentation/go.opentelemetry.io/otel => D:\rupam\go\auto-instrumentation\manual-mcp-server\demo\app\mcp\client\.otelc-build\instrumentation\go.opentelemetry.io\otel

replace go.opentelemetry.io/otelc/instrumentation/go.opentelemetry.io/otel/init => D:\rupam\go\auto-instrumentation\manual-mcp-server\demo\app\mcp\client\.otelc-build\instrumentation\go.opentelemetry.io\otel\init

replace go.opentelemetry.io/otelc/instrumentation/go.opentelemetry.io/otel/sdk/trace => D:\rupam\go\auto-instrumentation\manual-mcp-server\demo\app\mcp\client\.otelc-build\instrumentation\go.opentelemetry.io\otel\sdk\trace

replace go.opentelemetry.io/otelc/instrumentation/go.opentelemetry.io/otel/trace => D:\rupam\go\auto-instrumentation\manual-mcp-server\demo\app\mcp\client\.otelc-build\instrumentation\go.opentelemetry.io\otel\trace

replace go.opentelemetry.io/otelc/instrumentation/google.golang.org/grpc/client => D:\rupam\go\auto-instrumentation\manual-mcp-server\demo\app\mcp\client\.otelc-build\instrumentation\google.golang.org\grpc\client

replace go.opentelemetry.io/otelc/instrumentation/google.golang.org/grpc/server => D:\rupam\go\auto-instrumentation\manual-mcp-server\demo\app\mcp\client\.otelc-build\instrumentation\google.golang.org\grpc\server

replace go.opentelemetry.io/otelc/instrumentation/k8s.io/client-go => D:\rupam\go\auto-instrumentation\manual-mcp-server\demo\app\mcp\client\.otelc-build\instrumentation\k8s.io\client-go

replace go.opentelemetry.io/otelc/instrumentation/log => D:\rupam\go\auto-instrumentation\manual-mcp-server\demo\app\mcp\client\.otelc-build\instrumentation\log

replace go.opentelemetry.io/otelc/instrumentation/log/slog => D:\rupam\go\auto-instrumentation\manual-mcp-server\demo\app\mcp\client\.otelc-build\instrumentation\log\slog

replace go.opentelemetry.io/otelc/instrumentation/net/http/client => D:\rupam\go\auto-instrumentation\manual-mcp-server\demo\app\mcp\client\.otelc-build\instrumentation\net\http\client

replace go.opentelemetry.io/otelc/instrumentation/net/http/server => D:\rupam\go\auto-instrumentation\manual-mcp-server\demo\app\mcp\client\.otelc-build\instrumentation\net\http\server

replace go.opentelemetry.io/otelc/instrumentation/runtime => D:\rupam\go\auto-instrumentation\manual-mcp-server\demo\app\mcp\client\.otelc-build\instrumentation\runtime

replace go.opentelemetry.io/otelc/pkg => D:\rupam\go\auto-instrumentation\manual-mcp-server\demo\app\mcp\client\.otelc-build\pkg

replace go.opentelemetry.io/otelc/pkg/runtime => D:\rupam\go\auto-instrumentation\manual-mcp-server\demo\app\mcp\client\.otelc-build\pkg\runtime
