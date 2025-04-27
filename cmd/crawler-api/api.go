package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"sync/atomic"
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
	logger *logrus.Logger

	mu       sync.Mutex
	crawlers map[string]*crawler.Crawler

	connections connections
	fetcher     web.Fetcher
	visitor     web.Visitor
	tp          trace.TracerProvider
}

func (api *api) ApplyRoutes(r chi.Router) {
	r.Get("/status", api.status)
	r.Put("/crawl", api.crawl)
	r.Post("/start", api.start)
	r.Post("/stop/{host}", api.stop)

	// New (better) interface
	r.Post("/config", api.config)
	r.Patch("/{host}", api.update)
	r.Patch("/{host}/stop", api.stop)
	r.Delete("/{host}", api.delete)
	r.Post("/{host}/step", api.step)
}

func (api *api) Error(ctx context.Context, rw http.ResponseWriter, status int, err error) {
	StashError(ctx, err)
	api.logger.WithError(err).Error("error")
	b, err := json.Marshal(map[string]any{
		"error": err.Error(),
	})
	if err != nil {
		rw.WriteHeader(http.StatusInternalServerError)
		return
	}
	rw.WriteHeader(status)
	rw.Write(b)
}

type crawlerStatus struct {
	Host       string `json:"host"`
	Running    bool   `json:"running"`
	RoutingKey string `json:"routing_key,omitempty"`
	Wait       string `json:"wait"`
	Uptime     string `json:"uptime,omitempty"`
	Count      uint64 `json:"count"`
	MaxDepth   int32  `json:"max_depth"`
}

func initCrawlerStatus(status *crawlerStatus, crawler *crawler.Crawler) {
	status.Count = crawler.Count()
	status.Host = crawler.Host
	status.RoutingKey = routingKey(crawler.Host)
	status.Running = crawler.Running()
	status.Wait = crawler.Wait.String()
	uptime := time.Since(crawler.StartedAt())
	if !status.Running {
		uptime = time.Duration(0)
	}
	status.Uptime = uptime.String()
	status.MaxDepth = crawler.DepthLimit
}

func (api *api) status(rw http.ResponseWriter, r *http.Request) {
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
	for _, crawler := range api.crawlers {
		var status crawlerStatus
		initCrawlerStatus(&status, crawler)
		statuses = append(statuses, status)
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
	MaxDepth     int32    `json:"max_depth"`
	Host         string   `json:"host"`
	Wait         string   `json:"wait,omitempty"`
	NopVisitor   bool     `json:"nop_visitor"`
	ConsumerKeys []string `json:"consumer_keys"` // TODO add these keys to message queue consumer
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
	spider, err := api.GetOrCreateCrawler(ctx, params.Host)
	if err != nil {
		api.Error(ctx, rw, http.StatusInternalServerError, err)
		return
	}
	if spider.Running() {
		// api.Error(ctx, rw, http.StatusAccepted, crawler.ErrCrawlerRunning)
		var status crawlerStatus
		initCrawlerStatus(&status, spider)
		sendJSON(rw, http.StatusAccepted, &status)
		return
	}
	spider.DepthLimit = params.MaxDepth
	spider.Wait = wait
	if params.NopVisitor {
		spider.Visitor = &logVisitor{}
	}

	api.logger.Debug("starting crawler")
	err = spider.Start(api.ctx)
	if err != nil {
		api.Error(ctx, rw, http.StatusInternalServerError, err)
		return
	}

	api.AddCrawler(params.Host, spider)

	err = sendJSON(rw, 200, map[string]interface{}{
		"started":     spider.StartedAt(),
		"host":        spider.Host,
		"wait":        spider.Wait.String(),
		"running":     spider.Running(),
		"routing_key": routingKey(spider.Host),
	})
	if err != nil {
		api.Error(ctx, rw, 500, err)
		return
	}
}

type updateCrawlerRequest struct {
	Wait     *string `json:"wait"`
	MaxDepth *int32  `json:"max_depth"`
}

func (api *api) update(rw http.ResponseWriter, request *http.Request) {
	var (
		ctx  = request.Context()
		c    = chi.RouteContext(ctx)
		host = c.URLParams.Values[0]
		body updateCrawlerRequest
	)
	spider, ok := api.GetCrawler(host)
	if !ok {
		sendJSON(rw, http.StatusNotFound, map[string]string{})
		return
	}
	err := json.NewDecoder(request.Body).Decode(&body)
	if err != nil {
		api.Error(ctx, rw, http.StatusBadRequest, err)
		return
	}
	api.logger.WithFields(logrus.Fields{
		"max_depth": body.MaxDepth,
		"wait":      body.Wait,
	}).Info("updating crawler")
	if body.MaxDepth != nil {
		atomic.StoreInt32(&spider.DepthLimit, *body.MaxDepth)
	}
	var status crawlerStatus
	initCrawlerStatus(&status, spider)
	if body.Wait != nil {
		wait, err := time.ParseDuration(*body.Wait)
		if err != nil {
			api.Error(ctx, rw, http.StatusBadRequest, err)
			return
		}
		crawler.SetWait(wait).Apply(spider)
		status.Wait = wait.String()
	}
	sendJSON(rw, 200, &status)
}

func (api *api) delete(rw http.ResponseWriter, request *http.Request) {
	status := http.StatusOK
	host := chi.URLParam(request, "host")
	spider, ok := api.GetCrawler(host)
	if !ok {
		err := sendJSON(rw, http.StatusNotFound, map[string]string{})
		if err != nil {
			api.Error(request.Context(), rw, 500, err)
			return
		}
		return
	}
	api.mu.Lock()
	delete(api.crawlers, host)
	api.mu.Unlock()
	err := spider.Stop()
	if err != nil {
		api.Error(request.Context(), rw, 500, err)
		return
	}
	var cs crawlerStatus
	initCrawlerStatus(&cs, spider)
	err = sendJSON(rw, status, &cs)
	if err != nil {
		api.Error(request.Context(), rw, 500, err)
		return
	}
}

func (api *api) step(rw http.ResponseWriter, req *http.Request) {
	host := chi.URLParam(req, "host")
	spider, ok := api.GetCrawler(host)
	if !ok {
		err := sendJSON(rw, http.StatusNotFound, map[string]string{})
		if err != nil {
			api.Error(req.Context(), rw, 500, err)
			return
		}
		return
	}
	_ = spider
	rw.WriteHeader(http.StatusNotImplemented)
}

func (api *api) config(rw http.ResponseWriter, request *http.Request) {
	var (
		req  []ControlParams
		resp []crawlerStatus
		ctx  = request.Context()
	)
	err := json.NewDecoder(request.Body).Decode(&req)
	if err != nil {
		api.Error(ctx, rw, http.StatusBadRequest, err)
		return
	}
	resp = make([]crawlerStatus, 0, len(req))

	for _, conf := range req {
		wait, err := time.ParseDuration(conf.Wait)
		if err != nil {
			api.Error(ctx, rw, http.StatusBadRequest, errors.New("invalid wait string"))
			return
		}
		cr, err := api.GetOrCreateCrawler(ctx, conf.Host)
		if err != nil {
			if errors.Is(err, crawler.ErrCrawlerRunning) {
				api.logger.Info("crawler already created, skipping")
				continue
			} else {
				api.Error(ctx, rw, http.StatusInternalServerError, err)
				return
			}
		}
		cr.DepthLimit = conf.MaxDepth
		cr.Wait = wait
		if conf.NopVisitor {
			cr.Visitor = &logVisitor{}
		}
		err = cr.Start(api.ctx)
		if err != nil {
			if errors.Is(err, crawler.ErrCrawlerRunning) {
				api.logger.Info("crawler already created, skipping")
				goto response
			} else {
				api.Error(ctx, rw, http.StatusInternalServerError, err)
				return
			}
		}
		api.AddCrawler(conf.Host, cr)
	response:
		var status crawlerStatus
		initCrawlerStatus(&status, cr)
		resp = append(resp, status)
	}

	err = sendJSON(rw, 200, resp)
	if err != nil {
		api.Error(ctx, rw, 500, err)
		return
	}
}

func (api *api) stop(rw http.ResponseWriter, r *http.Request) {
	var (
		params ControlParams
		ctx    = r.Context()
		host   = chi.URLParamFromCtx(ctx, "host")
	)
	ctx = logging.Stash(ctx, api.logger)
	crawler, found := api.GetCrawler(host)
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
	err := crawler.Stop()
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
	ctx, span := tracer.Start(ctx, "api.get_crawler", trace.WithAttributes(
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
	set := storage.NewRedisURLSet(api.connections.redis)
	c := crawler.Crawler{
		Host:    host,
		Fetcher: api.fetcher,
		Logger:  api.logger,
		Tracer:  api.tp.Tracer("crawler-api.crawler"),
		Visitor: api.visitor,
		URLSet:  set,
		Filter:  &crawler.URLSetLinkFilter{URLSet: set},
	}
	return &c, api.initialize(ctx, &c)
}

func (api *api) initialize(ctx context.Context, c *crawler.Crawler) error {
	var err error
	c.Consumer, err = api.connections.bus.Consumer(
		c.Host,
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
		Robots:    c.RobotsCtrl,
		Logger:    api.logger,
	}
	return nil
}
