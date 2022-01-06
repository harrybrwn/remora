package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"

	"github.com/harrybrwn/config"
	"github.com/harrybrwn/remora/cmd"
	"github.com/harrybrwn/remora/internal/httputil"
	"github.com/harrybrwn/remora/internal/logging"
	"github.com/joho/godotenv"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.7.0"
	"go.opentelemetry.io/otel/trace"
)

type Config struct {
	Port       int
	ConfigFile string
	cmd.Config
}

func setup(conf *Config) error {
	log.SetLevel(logrus.DebugLevel)
	godotenv.Load()
	flag.IntVar(&conf.Port, "port", conf.Port, "run the server on a different port")
	flag.StringVar(&conf.ConfigFile, "config", conf.ConfigFile, "use a different config file")
	flag.Parse()

	config.SetConfig(&conf.Config)
	config.SetType("yaml")
	if conf.ConfigFile != "" {
		config.AddFilepath(conf.ConfigFile)
	} else {
		config.AddFile("config.yml")
		config.AddPath("/var/local/remora")
		config.AddPath(".")
	}
	config.InitDefaults()
	err := config.ReadConfig()
	if err != nil {
		return err
	}
	return nil
}

func StashError(ctx context.Context, err error) context.Context {
	return context.WithValue(ctx, httputil.ErrorContextKey, err)
}

func ErrorFromContext(ctx context.Context) error {
	res := ctx.Value(httputil.ErrorContextKey)
	if err, ok := res.(error); ok && err != nil {
		return err
	}
	return nil
}

func instrumentation(l logrus.FieldLogger, tracer trace.Tracer, serverName string) func(http.Handler) http.Handler {
	return func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			attrs := semconv.HTTPServerAttributesFromHTTPRequest(serverName, r.URL.Path, r)
			ctx, span := tracer.Start(
				r.Context(),
				fmt.Sprintf("%s %s", r.Method, r.RequestURI),
				trace.WithAttributes(attrs...),
			)
			defer span.End()

			resp := httputil.NewResponse(rw)
			h.ServeHTTP(resp, r.WithContext(ctx))

			logger := l
			err := ErrorFromContext(ctx)
			span.SetAttributes(semconv.HTTPStatusCodeKey.Int(resp.Status))
			if err != nil {
				span.RecordError(err)
				span.SetStatus(codes.Error, err.Error())
				logger = l.WithError(err)
			} else {
				span.SetStatus(codes.Ok, "request handled successfully")
			}
			logging.LogHTTPRequest(logger, resp, r)
		})
	}
}
