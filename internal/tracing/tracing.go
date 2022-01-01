package tracing

import (
	"errors"
	"fmt"

	"github.com/harrybrwn/remora/cmd"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/jaeger"
	"go.opentelemetry.io/otel/exporters/zipkin"
	"go.opentelemetry.io/otel/sdk/resource"
	tracesdk "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.4.0"
)

func Provider(cfg *cmd.TracerConfig, name string, opts ...tracesdk.TracerProviderOption) (*tracesdk.TracerProvider, error) {
	switch cfg.Type {
	case "jaeger":
		exporter, err := jaeger.New(jaeger.WithCollectorEndpoint(
			jaeger.WithEndpoint(cfg.Endpoint),
		))
		if err != nil {
			return nil, err
		}
		opts = append(opts, tracesdk.WithBatcher(exporter))
	case "zipkin":
		exporter, err := zipkin.New(cfg.Endpoint)
		if err != nil {
			return nil, err
		}
		opts = append(opts, tracesdk.WithSpanProcessor(tracesdk.NewBatchSpanProcessor(exporter)))
	case "datadog":
		return nil, errors.New("datadog tracing not supported")
	default:
		return nil, fmt.Errorf("tracing type %q not recognized", cfg.Type)
	}

	version := cmd.GetVersionInfo()
	opts = append(opts,
		tracesdk.WithResource(resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceNameKey.String(name), // TODO maybe use cfg.Name
			semconv.ServiceVersionKey.String(version.Version),
			semconv.TelemetrySDKLanguageGo,
			attribute.String("environment", "prod-dev"),
		)),
	)
	tp := tracesdk.NewTracerProvider(opts...)
	otel.SetTracerProvider(tp)
	return tp, nil
}
