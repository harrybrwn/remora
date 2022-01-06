package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"testing"
	"time"

	"github.com/harrybrwn/remora/event"
	"github.com/harrybrwn/remora/frontier"
	"github.com/harrybrwn/remora/internal/visitor"
	"github.com/harrybrwn/remora/storage"
	"github.com/harrybrwn/remora/web"
	"github.com/sirupsen/logrus"
	"github.com/streadway/amqp"
	"google.golang.org/protobuf/proto"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/jaeger"
	"go.opentelemetry.io/otel/sdk/resource"
	"go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.7.0"
)

func Test(t *testing.T) {
}

type vis struct{}

func (*vis) Filter(*web.PageRequest, *url.URL) error { return nil }
func (*vis) Visit(_ context.Context, p *web.Page)    { log.Infof("visit %s", p.URL.String()) }
func (*vis) LinkFound(*url.URL) error                { return nil }

func TestSpider(t *testing.T) {
	t.Skip()
	log.SetLevel(logrus.TraceLevel)
	web.SetLogger(log)
	visitor.SetLogger(log)
	ctx := context.Background()
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()
	// opts := badger.DefaultOptions("")
	// opts.Logger = nil
	// opts.InMemory = true
	// db, err := badger.Open(opts)
	// if err != nil {
	// 	t.Fatal(err)
	// }
	// bus := eventqueue.New(db)
	bus := event.NewChannelBus()
	defer func() {
		// close bus before db
		bus.Close()
		// db.Close()
	}()
	req := web.PageRequest{
		URL:   "https://en.wikipedia.org/wiki/Main_Page",
		Depth: 0,
	}
	raw, err := proto.Marshal(&req)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		<-time.After(time.Second * 1)
		pub, err := bus.Publisher()
		if err != nil {
			log.WithError(err).Error("could not create publisher")
			return
		}
		// defer pub.Close()
		log.Info("publishing seed url")
		err = pub.Publish("en.wikipedia.org", amqp.Publishing{Body: raw})
		if err != nil {
			t.Error(err)
			cancel()
		}
	}()

	ctx, cancel = context.WithTimeout(ctx, time.Second*5)
	defer cancel()
	s := &spider{
		// Wait:    time.Millisecond * 500 * 2 * 10,
		Wait:    time.Second * 5,
		Host:    "en.wikipedia.org",
		Visitor: &vis{},
		URLSet:  storage.NewInMemoryURLSet(),
		Bus:     bus,
	}
	go func() {
		// err = s.start(ctx)
		// if err != nil {
		// 	t.Error(err)
		// 	cancel()
		// }
		defer cancel()
		s.robots, err = web.GetRobotsTxT(ctx, s.Host)
		if err != nil {
			panic(err)
		}
		s.init()
		publisher, err := s.Bus.Publisher(event.PublishWithContext(ctx))
		if err != nil {
			panic(err)
		}
		consumer, err := s.Bus.Consumer(
			s.Host,
			event.ConsumeWithContext(ctx),
			event.WithPrefetch(s.Prefetch),
			event.WithKeys(s.hostHashKey()),
			frontier.WithAutoAck(false), // for rabbitmq message busses
		)
		if err != nil {
			panic(err)
		}
		defer consumer.Close()
		deliveries, err := consumer.Consume()
		if err != nil {
			panic(err)
		}
		go func() {
			for range s.sleep {
			}
		}()
		for {
			select {
			case <-ctx.Done():
				log.Info("context cancelled")
				return
			case msg, ok := <-deliveries:
				if !ok {
					return
				}
				var (
					req     web.PageRequest
					msgType = frontier.ParseMessageType(msg.Type)
				)
				if msgType != frontier.PageRequest {
					// This message was not meant for use, ignore it and don't ack
					log.Warn("skipping message: not a page request message")
				}
				err = proto.Unmarshal(msg.Body, &req)
				if err != nil {
					log.WithError(err).Error("could not unmarshal page request")
					continue
				}
				log.WithFields(logrus.Fields{
					"key": msg.RoutingKey, "tag": msg.DeliveryTag,
					"exchange": msg.Exchange, "type": msg.Type,
					"consumer": msg.ConsumerTag, "depth": req.Depth,
					"url": req.URL,
				}).Trace("request")
				err := s.handle(ctx, &req, publisher)
				if err == nil {
					if err = msg.Ack(false); err != nil {
						log.WithFields(logrus.Fields{
							"error": err,
						}).Warning("could not acknowledge message")
					}
				}
			}
		}
	}()
	<-ctx.Done()
}

func TestTracing(t *testing.T) {
	const (
		service     = "tracing-test"
		environment = "production"
		id          = 1
	)
	exp, err := jaeger.New(jaeger.WithCollectorEndpoint(jaeger.WithEndpoint("http://localhost:14268/api/traces")))
	if err != nil {
		t.Fatal(err)
	}
	tp := trace.NewTracerProvider(
		trace.WithBatcher(exp),
		trace.WithResource(resource.NewWithAttributes(
			semconv.SchemaURL,
			semconv.ServiceNameKey.String(service),
			attribute.String("environment", environment),
			attribute.Int64("ID", id),
		)),
	)
	otel.SetTracerProvider(tp)
	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	defer func(ctx context.Context) {
		// Do not make the application hang when it is shutdown.
		ctx, cancel = context.WithTimeout(ctx, time.Second*5)
		defer cancel()
		if err := tp.Shutdown(ctx); err != nil {
			log.Fatal(err)
		}
	}(ctx)

	tr := tp.Tracer("test-component")
	ctx, span := tr.Start(ctx, "foo")
	defer span.End()
	time.Sleep(time.Second * 5)
	fmt.Println(ctx)
}
