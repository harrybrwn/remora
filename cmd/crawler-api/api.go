package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/harrybrwn/remora/crawler"
	"github.com/harrybrwn/remora/event"
	"github.com/harrybrwn/remora/internal/logging"
	"github.com/harrybrwn/remora/internal/que"
	"github.com/harrybrwn/remora/storage"
	"github.com/harrybrwn/remora/web"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type api struct {
	ctx    context.Context
	logger logrus.FieldLogger

	mu       sync.Mutex
	crawlers map[string]*crawler.Crawler

	connections connections
	fetcher     web.Fetcher
	visitor     web.Visitor
	tp          trace.TracerProvider
}

func (api *api) ApplyRoutes(r chi.Router) {
	r.Get("/status", api.status)
	r.Get("/crawl", api.crawl)
	r.Post("/start", api.start)
	r.Post("/stop", api.stop)
}

func (api *api) Error(ctx context.Context, rw http.ResponseWriter, status int, err error) {
	StashError(ctx, err)
	api.logger.WithError(err).Error("error")
	b, err := json.Marshal(map[string]interface{}{
		"error": err.Error(),
	})
	if err != nil {
		rw.WriteHeader(http.StatusInternalServerError)
		return
	}
	rw.WriteHeader(status)
	rw.Write(b)
}

func (api *api) status(rw http.ResponseWriter, r *http.Request) {
	type crawlerStatus struct {
		Host       string `json:"host"`
		Running    bool   `json:"running"`
		RoutingKey string `json:"routing_key,omitempty"`
		Uptime     string `json:"uptime"`
		Count      uint64 `json:"count"`
	}
	var (
		statuses = make([]crawlerStatus, 0, len(api.crawlers))
		m        = make(map[string]interface{})
	)

	if len(api.crawlers) == 0 {
		m["status"] = "ready"
	} else {
		m["status"] = "ok"
	}
	api.mu.Lock()
	for host, crawler := range api.crawlers {
		running := crawler.Running()
		uptime := time.Since(crawler.StartedAt())
		if !running {
			uptime = time.Duration(0)
		}
		statuses = append(statuses, crawlerStatus{
			Host:    host,
			Running: running,
			Uptime:  fmt.Sprintf("%v", uptime),
			Count:   crawler.Count(),
		})
	}
	api.mu.Unlock()
	m["crawlers"] = statuses

	b, err := json.MarshalIndent(m, "", "    ")
	if err != nil {
		api.Error(r.Context(), rw, http.StatusBadRequest, err)
		return
	}
	rw.Write(b)
}

type ControlParams struct {
	MaxDepth   int    `json:"max_depth"`
	Host       string `json:"host"`
	Wait       string `json:"wait,omitempty"`
	NopVisitor bool   `json:"nop_visitor"`
}

func (api *api) start(rw http.ResponseWriter, r *http.Request) {
	var (
		params ControlParams
		wait   time.Duration
		ctx    = r.Context()
	)
	err := json.NewDecoder(r.Body).Decode(&params)
	if err != nil {
		api.Error(ctx, rw, http.StatusBadRequest, err)
		return
	}
	if len(params.Wait) != 0 {
		wait, err = time.ParseDuration(params.Wait)
		if err != nil {
			api.Error(ctx, rw, http.StatusBadRequest, errors.New("invalid wait string"))
			return
		}
	}
	crawler, err := api.GetOrCreateCrawler(ctx, params.Host)
	if err != nil {
		api.Error(ctx, rw, http.StatusInternalServerError, err)
		return
	}
	crawler.DepthLimit = params.MaxDepth
	crawler.Wait = wait
	if params.NopVisitor {
		crawler.Visitor = &logVisitor{}
	}
	if crawler.Running() {
		err = errors.New("crawler is already running")
		api.Error(ctx, rw, http.StatusBadRequest, err)
		return
	}
	api.logger.Debug("starting crawler")
	err = crawler.Start(api.ctx)
	if err != nil {
		api.Error(ctx, rw, http.StatusInternalServerError, err)
		return
	}

	api.AddCrawler(params.Host, crawler)

	err = sendJSON(rw, 200, map[string]interface{}{
		"started":     time.Now(),
		"host":        crawler.Host,
		"wait":        crawler.Wait.String(),
		"running":     crawler.Running(),
		"routing_key": routingKey(crawler.Host),
	})
	if err != nil {
		api.Error(ctx, rw, 500, err)
		return
	}
}

func (api *api) stop(rw http.ResponseWriter, r *http.Request) {
	var (
		params ControlParams
		ctx    = logging.Stash(r.Context(), api.logger)
	)
	err := json.NewDecoder(r.Body).Decode(&params)
	if err != nil {
		api.Error(ctx, rw, http.StatusBadRequest, err)
		return
	}
	if params.Host == "" {
		params.Host = r.URL.Query().Get("host")
	}
	crawler, found := api.GetCrawler(params.Host)
	if !found {
		api.Error(
			ctx, rw, http.StatusNotFound,
			fmt.Errorf("could not find crawler for %q", params.Host),
		)
		return
	}
	if !crawler.Running() {
		api.Error(
			ctx, rw, http.StatusConflict,
			fmt.Errorf("crawler for %q already running", params.Host),
		)
		return
	}
	err = crawler.Stop()
	if err != nil {
		api.Error(ctx, rw, 500, err)
		return
	}
}

type crawlResponse struct {
	URL            string   `json:"url"`
	Redirected     bool     `json:"redirected"`
	RedirectedFrom string   `json:"redirected_from,omitempty"`
	Status         int      `json:"status"`
	ContentType    string   `json:"content_type"`
	Encoding       string   `json:"encoding"`
	Hash           string   `json:"hash"`
	Links          []string `json:"links"`
}

func (api *api) crawl(rw http.ResponseWriter, r *http.Request) {
	ctx := logging.Stash(r.Context(), api.logger)
	u, err := url.Parse(r.URL.Query().Get("url"))
	if err != nil {
		api.Error(ctx, rw, http.StatusBadRequest, err)
		return
	}
	page, err := api.fetcher.Fetch(ctx, web.NewPageRequest(u, 0))
	if err != nil {
		api.Error(ctx, rw, http.StatusInternalServerError, err)
		return
	}
	crawler, found := api.GetCrawler(page.URL.Host)
	if found {
		err = crawler.Publisher.Publish(ctx, page.Depth, page.Links)
		if err != nil {
			api.Error(ctx, rw, http.StatusInternalServerError, err)
			return
		}
	} else {
		api.Error(ctx, rw, http.StatusBadRequest, errors.New("could not find crawler"))
		return
	}
	api.visitor.Visit(ctx, page)
	links := make([]string, len(page.Links))
	for i, l := range page.Links {
		links[i] = l.String()
	}
	var redirectedFrom string
	if page.RedirectedFrom != nil {
		redirectedFrom = page.RedirectedFrom.String()
	}
	err = sendJSON(rw, 200, &crawlResponse{
		URL:            page.URL.String(),
		Redirected:     page.Redirected,
		RedirectedFrom: redirectedFrom,
		Status:         page.Status,
		ContentType:    page.ContentType,
		Encoding:       page.Encoding,
		Hash:           hex.EncodeToString(page.Hash[:]),
		Links:          links,
	})
	if err != nil {
		api.Error(ctx, rw, http.StatusInternalServerError, err)
		return
	}
}

func (api *api) GetCrawler(host string) (*crawler.Crawler, bool) {
	api.mu.Lock()
	cr, found := api.crawlers[host]
	api.mu.Unlock()
	return cr, found
}

func (api *api) AddCrawler(host string, c *crawler.Crawler) {
	api.mu.Lock()
	if _, ok := api.crawlers[host]; !ok {
		api.crawlers[host] = c
	}
	api.mu.Unlock()
}

func (api *api) GetOrCreateCrawler(ctx context.Context, host string) (*crawler.Crawler, error) {
	tracer := api.tp.Tracer("crawler-api")
	_, span := tracer.Start(ctx, "api.get_crawler", trace.WithAttributes(
		attribute.Key("host").String(host),
	))
	defer span.End()
	cr, found := api.GetCrawler(host)
	span.SetAttributes(attribute.Key("api.has_crawler").Bool(found))
	if found {
		log.Info("found crawler")
		return cr, api.initialize(ctx, cr)
	}
	log.Info("creating new crawler")
	return api.createCrawler(ctx, host)
}

func (api *api) createCrawler(ctx context.Context, host string) (*crawler.Crawler, error) {
	c := crawler.Crawler{
		Host:    host,
		Fetcher: api.fetcher,
		Logger:  api.logger,
		Tracer:  api.tp.Tracer("crawler-api.crawler"),
		Visitor: api.visitor,
	}
	return &c, api.initialize(ctx, &c)
}

func (api *api) initialize(ctx context.Context, c *crawler.Crawler) error {
	api.logger.Debug("initializing crawler")
	defer api.logger.Debug("done initializing crawler")
	var err error
	c.Consumer, err = api.connections.bus.Consumer(
		routingKey(c.Host),
		que.WithConsumerQueueOpts(&que.QueueDeclareOptions{
			Durable: true,
			Args:    que.AMQPTable{"x-queue-mode": "lazy"},
		}),
		que.WithConsumeOptions(&que.ConsumeOptions{AutoAck: false}),
		event.WithKeys(routingKey(c.Host)),
		event.WithPrefetch(10),
	)
	if err != nil {
		return errors.Wrap(err, "failed to create consumer")
	}
	c.URLSet = storage.NewRedisURLSet(api.connections.redis)
	c.RobotsCtrl, err = web.NewRobotCtrl(ctx, c.Host, []string{"*", web.UserAgent})
	if err != nil {
		return errors.Wrap(err, "failed to fetch robots.txt")
	}
	publisher, err := api.connections.bus.Publisher()
	if err != nil {
		return errors.Wrap(err, "failed to create publisher")
	}
	c.Publisher = &crawler.PagePublisher{
		Publisher: publisher,
		URLSet:    c.URLSet,
		Robots:    c.RobotsCtrl,
		Logger:    api.logger,
	}
	return nil
}
