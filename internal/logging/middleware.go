package logging

import (
	"context"
	"net/http"
	"time"

	"github.com/harrybrwn/remora/internal/httputil"
	"github.com/sirupsen/logrus"
)

type loggingContextKeyType struct{}

var loggingContextKey loggingContextKeyType

func Stash(ctx context.Context, logger logrus.FieldLogger) context.Context {
	return context.WithValue(ctx, loggingContextKey, logger)
}

func FromContext(ctx context.Context) logrus.FieldLogger {
	val := ctx.Value(loggingContextKey)
	if val == nil {
		return logrus.StandardLogger()
	}
	l, ok := val.(logrus.FieldLogger)
	if !ok {
		return logrus.StandardLogger()
	}
	return l
}

func LogHTTPRequests(l logrus.FieldLogger) func(http.Handler) http.Handler {
	return func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
			var (
				resp  = httputil.NewResponse(rw)
				start = time.Now()
			)
			h.ServeHTTP(resp, r)
			LogHTTPRequest(l.WithField("latency", time.Since(start)), resp, r)
		})
	}
}

func LogHTTPRequest(
	l logrus.FieldLogger,
	resp *httputil.Response,
	req *http.Request,
) {
	l = l.WithFields(logrus.Fields{
		"status":         resp.Status,
		"method":         req.Method,
		"content_length": req.ContentLength,
		"user_agent":     req.Header.Get("User-Agent"),
		"uri":            req.RequestURI,
	})
	var lg func(string, ...interface{})
	if resp.Status < 300 {
		lg = l.Infof
	} else if resp.Status < 400 {
		lg = l.Infof
	} else if resp.Status < 500 {
		lg = l.Warnf
	} else if resp.Status >= 500 {
		lg = l.Errorf
	}
	lg("request")
}
