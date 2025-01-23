package region

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const MaxKeySize = 65000

type Wrapper interface {
	Wrap(ctx context.Context, name string, fn RegionFn)
}

type Region struct {
	tracer      trace.Tracer
	attrs       []attribute.KeyValue
	parent      trace.Span
	parentAttrs []attribute.KeyValue
}

func NewRegion(tracer trace.Tracer, attrs ...attribute.KeyValue) *Region {
	return &Region{
		tracer: tracer,
		attrs:  attrs,
	}
}

type RegionFn func(ctx context.Context) ([]attribute.KeyValue, error)

func (s *Region) Wrap(ctx context.Context, name string, fn RegionFn) {
	opts := []trace.SpanStartOption{
		trace.WithAttributes(prepAttrs(s.attrs)...),
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
			span.SetAttributes(prepAttrs(attrs)...)
		}
		span.SetStatus(codes.Ok, fmt.Sprintf("%q succeeded", name))
	}
}

func (s *Region) WithParent(span trace.Span, attrs ...attribute.KeyValue) *Region {
	cp := s.Copy()
	cp.parent = span
	cp.parentAttrs = prepAttrs(attrs)
	return s
}

func (s *Region) Attr(attrs ...attribute.KeyValue) *Region {
	cp := s.Copy()
	cp.attrs = append(cp.attrs, prepAttrs(attrs)...)
	return cp
}

func (s *Region) Copy() *Region {
	cp := Region{
		tracer:      s.tracer,
		parent:      s.parent,
		attrs:       prepAttrs(s.attrs),
		parentAttrs: prepAttrs(s.parentAttrs),
	}
	return &cp
}

func prepAttrs(attrs []attribute.KeyValue) []attribute.KeyValue {
	res := make([]attribute.KeyValue, 0, len(attrs))
	for _, attr := range attrs {
		switch attr.Value.Type() {
		case attribute.STRING:
			v := attr.Value.AsString()
			if len(v) > MaxKeySize {
				attr = attr.Key.String(v[:MaxKeySize-1])
			}
		}
		res = append(res, attr)
	}
	return res
}
