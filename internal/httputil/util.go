package httputil

import (
	"context"
	"net/http"
)

type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

type Response struct {
	http.ResponseWriter
	Status int
}

func (r *Response) WriteHeader(status int) {
	r.Status = status
	r.ResponseWriter.WriteHeader(status)
}

func NewResponse(rw http.ResponseWriter) *Response {
	return &Response{
		ResponseWriter: rw,
		Status:         200,
	}
}

type errorContextKeyType struct{}

var ErrorContextKey errorContextKeyType

func StashError(ctx context.Context, err error) context.Context {
	return context.WithValue(ctx, ErrorContextKey, err)
}

func ErrorFromContext(ctx context.Context) error {
	res := ctx.Value(ErrorContextKey)
	if err, ok := res.(error); ok && err != nil {
		return err
	}
	return nil
}
