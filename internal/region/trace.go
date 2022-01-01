package region

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type Wrapper interface {
	Wrap(ctx context.Context, name string, fn Region)
}

type Span struct {
	tracer      trace.Tracer
	attrs       []attribute.KeyValue
	parent      trace.Span
	parentAttrs []attribute.KeyValue
}

func New(tracer trace.Tracer, attrs ...attribute.KeyValue) *Span {
	return &Span{
		tracer: tracer,
		attrs:  attrs,
	}
}

type Region func(ctx context.Context) ([]attribute.KeyValue, error)

func (s *Span) Wrap(ctx context.Context, name string, fn Region) {
	opts := []trace.SpanStartOption{
		trace.WithAttributes(s.attrs...),
	}
	if s.parent != nil {
		// Link to parent
		opts = append(opts, trace.WithLinks(trace.Link{
			SpanContext: s.parent.SpanContext(),
			Attributes:  s.parentAttrs,
		}))
	}

	regionCtx, span := s.tracer.Start(ctx, name, opts...)
	defer span.End()
	attrs, err := fn(regionCtx)
	if err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, err.Error())
	} else {
		if len(attrs) > 0 {
			span.SetAttributes(attrs...)
		}
		span.SetStatus(codes.Ok, fmt.Sprintf("%q succeeded", name))
	}
}

func (s *Span) WithParent(span trace.Span, attrs ...attribute.KeyValue) *Span {
	cp := s.Copy()
	cp.parent = span
	cp.parentAttrs = attrs
	return s
}

func (s *Span) Attr(attrs ...attribute.KeyValue) *Span {
	cp := s.Copy()
	cp.attrs = append(cp.attrs, attrs...)
	return cp
}

func (s *Span) Copy() *Span {
	cp := Span{
		tracer:      s.tracer,
		parent:      s.parent,
		attrs:       make([]attribute.KeyValue, len(s.attrs)),
		parentAttrs: make([]attribute.KeyValue, len(s.parentAttrs)),
	}
	copy(cp.attrs, s.attrs)
	copy(cp.parentAttrs, s.parentAttrs)
	return &cp
}
