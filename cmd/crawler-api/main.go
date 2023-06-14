package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-redis/redis/extra/redisotel/v8"
	"github.com/go-redis/redis/v8"
	"github.com/harrybrwn/remora/cmd"
	"github.com/harrybrwn/remora/crawler"
	"github.com/harrybrwn/remora/db"
	"github.com/harrybrwn/remora/internal/logging"
	"github.com/harrybrwn/remora/internal/que"
	"github.com/harrybrwn/remora/internal/tracing"
	"github.com/harrybrwn/remora/internal/visitor"
	"github.com/harrybrwn/remora/web"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/trace"
)

var log = logrus.StandardLogger()

const (
	component = "crawler-api"
	exchange  = "page_topic"
)

func main() {
	var (
		conf = Config{Port: 3010}
		r    = chi.NewRouter()
	)
	if err := setup(&conf); err != nil {
		log.Fatal(err)
	}
	tracing.Init(&conf.Tracer, component)

	v := cmd.GetVersionInfo()
	web.UserAgent = fmt.Sprintf("Remora/%s", v.Version)

	var (
		err   error
		ctx   = logging.Stash(context.Background(), log)
		conns = connections{bus: &que.MessageQueue{Exchange: &que.Exchange{
			Name:    "page_topic",
			Kind:    que.ExchangeTopic,
			Durable: true,
		}}}
		fetcher = web.NewFetcher(
			web.UserAgent,
			web.WithTransport(transport(tracing.Default())),
		)
	)
	conf.DB.Logger = log
	if err = conns.Connect(ctx, &conf.Config); err != nil {
		log.Fatal(err)
	}
	log.Level = logrus.TraceLevel
	vis, err := visitor.New(conns.db, conns.redis)
	if err != nil {
		log.Fatal(err)
	}
	api := api{
		ctx:         ctx, // cancellation for workers
		logger:      log,
		fetcher:     fetcher,
		visitor:     vis,
		crawlers:    make(map[string]*crawler.Crawler),
		tp:          tracing.Default(),
		connections: conns,
	}

	r.Use(instrumentation(api.logger, tracing.Default().Tracer(component), component))
	api.ApplyRoutes(r)
	api.logger.Info("starting server")
	listenAndServe(conf.Port, r)
}

func listenAndServe(port int, h http.Handler) error {
	addr := net.JoinHostPort("", strconv.Itoa(port))
	log.WithField("address", addr).Info("starting server")
	err := http.ListenAndServe(addr, h)
	if err != nil {
		log.Error(err)
	}
	return err
}

func routingKey(host string) string {
	hash := fnv.New128()
	io.WriteString(hash, host)
	return fmt.Sprintf("%x.%s", hash.Sum(nil)[:2], host)
}

func sendJSON(rw http.ResponseWriter, status int, v interface{}) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	rw.WriteHeader(status)
	rw.Write(b)
	return nil
}

type connections struct {
	redis *redis.Client
	db    *sql.DB
	bus   *que.MessageQueue
}

func (c *connections) Connect(ctx context.Context, conf *cmd.Config) error {
	var (
		wg     sync.WaitGroup
		errs   = make(chan error)
		logger = logging.FromContext(ctx)
	)
	type connect func() (string, error)
	connectors := []connect{
		func() (string, error) {
			var err error
			c.redis, err = waitForRedisClient(ctx, conf.RedisOpts())
			if err == nil {
				c.redis.AddHook(redisotel.NewTracingHook())
			}
			return "redis", err
		},
		func() (string, error) {
			var err error
			c.db, err = db.NewWithTimeout(ctx, time.Second*30, &conf.DB)
			// c.db, err = db.New(&conf.DB)
			return "database", err
		},
		func() (string, error) {
			return "rabbitmq", c.bus.Connect(conf.MessageQueue.URI())
		},
	}
	wg.Add(len(connectors))
	go func() {
		wg.Wait()
		close(errs)
	}()
	for _, fn := range connectors {
		go func(fn connect) {
			defer wg.Done()
			s := time.Now()
			name, err := fn()
			if err != nil {
				logger.WithFields(logrus.Fields{
					"error":  err,
					"client": name,
				}).Error("failed to connect")
				errs <- err
				return
			}
			logger.WithFields(logrus.Fields{
				"client": name,
				"time":   time.Since(s),
			}).Info("connected")
		}(fn)
	}
	return <-errs
}

func transport(tp trace.TracerProvider) http.RoundTripper {
	httpTransport := otelhttp.NewTransport(
		web.DefaultTransport,
		otelhttp.WithSpanOptions(trace.WithSpanKind(trace.SpanKindClient)),
		otelhttp.WithTracerProvider(tp),
	)
	http.DefaultClient.Transport = httpTransport
	http.DefaultTransport = httpTransport
	web.HttpClient.Transport = httpTransport
	return httpTransport
}

func waitForRedisClient(ctx context.Context, opts *redis.Options) (c *redis.Client, err error) {
	ctx, cancel := context.WithTimeout(ctx, time.Second*20)
	defer cancel()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	c = redis.NewClient(opts)
	if err = c.Ping(ctx).Err(); err == nil {
		return c, nil
	}
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			c = redis.NewClient(opts)
			if err = c.Ping(ctx).Err(); err == nil {
				return c, nil
			}
		}
	}
}

type logVisitor struct{}

func (*logVisitor) LinkFound(u *url.URL) error {
	log.WithField("stage", "link-found").Debug(u)
	return nil
}
func (*logVisitor) Filter(_ *web.PageRequest, u *url.URL) error {
	log.WithField("stage", "filter").Info(u)
	return nil
}
func (*logVisitor) Visit(_ context.Context, p *web.Page) { log.WithField("stage", "visit").Info(p.URL) }
