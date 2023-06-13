package crawler

import (
	"context"
	"fmt"
	"net/url"

	"github.com/harrybrwn/remora/event"
	"github.com/harrybrwn/remora/frontier"
	"github.com/harrybrwn/remora/internal/logging"
	"github.com/harrybrwn/remora/web"
	"github.com/sirupsen/logrus"
	"github.com/streadway/amqp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type LinkPublisher interface {
	Publish(ctx context.Context, depth uint32, links []*url.URL) error
	Close() error
}

type PagePublisher struct {
	Robots    web.RobotsController
	Publisher event.Publisher
	Logger    logrus.FieldLogger
}

func (pp *PagePublisher) Close() error { return pp.Publisher.Close() }

func (pp *PagePublisher) Publish(ctx context.Context, depth uint32, links []*url.URL) error {
	var (
		err    error
		done   = ctx.Done()
		length = len(links)
		logger = logging.FromContext(ctx)
		tracer = otel.Tracer("crawler")
	)
	_, span := tracer.Start(
		ctx, "crawler.publish_links",
		trace.WithSpanKind(trace.SpanKindProducer),
		trace.WithAttributes(
			attribute.Key("links.length").Int(length),
		),
	)
	defer span.End()

	if length <= 0 {
		return nil
	}
	for _, l := range links {
		select {
		case <-done:
			return nil
		default:
		}
		switch l.Scheme {
		case
			"javascript",
			"mailto",
			"tel",
			"":
			continue
		default:
		}
		if pp.Robots.ShouldSkip(l) {
			span.AddEvent(
				"found in robots.txt",
				trace.WithAttributes(attribute.Key("link").String(l.String())),
			)
			continue
		}
		req := web.NewPageRequest(l, depth+1)
		key := fmt.Sprintf("%x.%s", req.Key[:2], l.Host)
		msg := amqp.Publishing{
			DeliveryMode: amqp.Persistent,
			Priority:     0,
		}
		err = frontier.SetPageReqAsMessageBody(req, &msg)
		if err != nil {
			logger.WithError(err).Error("could not marshal new page request")
			continue
		}
		err = pp.Publisher.Publish(key, msg)
		if err != nil {
			logger.WithError(err).Error("could not publish new message")
			continue
		}
	}
	return nil
}
