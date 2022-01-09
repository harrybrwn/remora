package crawler

import (
	"context"
	"encoding/hex"
	"fmt"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/harrybrwn/remora/event"
	"github.com/harrybrwn/remora/internal/logging"
	"github.com/harrybrwn/remora/internal/que"
	"github.com/harrybrwn/remora/storage"
	"github.com/harrybrwn/remora/web"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/proto"
)

var (
	ErrCrawlerRunning = errors.New("crawler is already running")
	ErrCrawlerStopped = errors.New("crawler is already stopped")
)

type Crawler struct {
	// Required
	Host       string
	URLSet     storage.URLSet
	Fetcher    web.Fetcher
	RobotsCtrl web.RobotsController
	Consumer   event.Consumer
	Visitor    web.Visitor
	Publisher  LinkPublisher
	// Optional
	Wait       time.Duration // defaults to 5 seconds
	DepthLimit int           // DepthLimit of zero means limit is not enforced
	Logger     logrus.FieldLogger
	Tracer     trace.Tracer

	// State for maintaining the receive loop
	msgs   <-chan que.Delivery
	ticker *time.Ticker

	// startedAt is the time that the crawler was started
	startedAt time.Time
	// Running can either be 0 or 1 and should only be written
	// or read using atomic operations.
	running uint32
	// Count is a counter on the number of pages received
	count uint64
	// This channel is for graceful shutdowns
	stop chan struct{}
	wait chan time.Duration

	// This tells outside callers that one site crawl has finished.
	// This should happen when the crawl depth limit is reached.
	done chan struct{}
}

func (c *Crawler) init() (err error) {
	if c.URLSet == nil {
		return errors.New("no url set")
	}
	if c.Fetcher == nil {
		return errors.New("no page fetcher")
	}
	if c.Host == "" {
		return errors.New("no hostname given to crawl")
	}
	if c.RobotsCtrl == nil {
		return errors.New("no robots.txt controller")
	}
	if c.Consumer == nil {
		return errors.New("no consumer")
	}
	if c.Wait == 0 {
		c.Wait = time.Second * 5
	}
	if c.Logger == nil {
		c.Logger = logrus.StandardLogger()
	}
	if c.Tracer == nil {
		c.Tracer = otel.Tracer("crawler")
	}
	c.msgs, err = c.Consumer.Consume()
	if err != nil {
		return err
	}
	c.Fetcher = &timeoutFetcher{
		Fetcher: c.Fetcher,
		Timeout: time.Minute,
	}
	c.stop = make(chan struct{})
	c.wait = make(chan time.Duration)
	return nil
}

func (c *Crawler) Start(ctx context.Context) (err error) {
	if c.Running() {
		return ErrCrawlerRunning
	}
	if err = c.init(); err != nil {
		return errors.Wrap(err, "could not start crawler")
	}
	c.startedAt = time.Now()
	atomic.StoreUint32(&c.running, 1)
	go c.loop(ctx)
	return nil
}

func (c *Crawler) Stop() error {
	if !c.Running() {
		return ErrCrawlerStopped
	}
	close(c.stop)
	atomic.StoreUint32(&c.running, 0)
	return nil
}

func (c *Crawler) StartedAt() time.Time {
	return c.startedAt
}

func (c *Crawler) Count() uint64 { return atomic.LoadUint64(&c.count) }

func (c *Crawler) Running() bool {
	return atomic.LoadUint32(&c.running) == 1
}

func (c *Crawler) NotifyDone(done chan struct{}) <-chan struct{} {
	c.done = done
	return done
}

var errCrawlerFatal = errors.New("crawler has been stopped")

func (c *Crawler) loop(ctx context.Context) {
	// TODO when using a timer to enforce politness, the crawler may start too many
	// goroutines that all try to fetch from the same host but do not finish for
	// whatever reason. Maybe the website host we are crawling is very slow, in that
	// case it will be useful to have some sort of throttling mechanism that will
	// slow the crawler down automatically.
	c.ticker = time.NewTicker(c.Wait)
	defer func() {
		c.ticker.Stop()
		atomic.StoreUint32(&c.running, 0)
		if c.done != nil {
			c.done <- struct{}{}
		}
	}()

	fn := func() {
		err := c.recv(ctx)
		if err != nil {
			if err != errCrawlerFatal {
				c.Logger.WithError(err).Error("stopping crawler loop")
			}
			close(c.stop)
		}
	}

	go fn()
	for {
		select {
		case <-c.stop:
			return
		case <-ctx.Done():
			return
		case d := <-c.wait:
			c.ticker.Reset(d)
			c.Wait = d
		case <-c.ticker.C:
			go fn()
		}
	}
}

const (
	KeyCrawlerHost       = attribute.Key("crawler.host")
	KeyCrawlerDepthLimit = attribute.Key("crawler.depth_limit")
	KeyCrawlerWait       = attribute.Key("crawler.wait")
	KeyPageRequestURL    = attribute.Key("page_request.url")
	KeyPageRequestDepth  = attribute.Key("page_request.depth")
	KeyPageRequestKey    = attribute.Key("page_request.key")
	KeyLink              = attribute.Key("link")

	EventInRobotsTXT      = "Found in robots.txt"
	EventInURLSet         = "URL found in urlset"
	EventRedirected       = "Page was redirected"
	EventContextCancelled = "Context cancelled"
)

// recv is the message receive and decode stage. It should only ever return an error
// if the crawl loop must end.
func (c *Crawler) recv(ctx context.Context) error {
	select {
	case <-c.stop:
		c.Logger.Warn("crawler stopped")
		return errCrawlerFatal
	case <-ctx.Done():
		c.Logger.Warn("crawler stopped: context cancelled")
		return ctx.Err()
	case msg, ok := <-c.msgs:
		if !ok {
			err := errors.New("messages channel closed")
			c.Logger.WithError(err).Error("consumer channel was closed")
			return err
		}
		ctx, span := c.Tracer.Start(
			ctx, fmt.Sprintf("crawler.receive %s", c.Host),
			trace.WithSpanKind(trace.SpanKindConsumer),
			trace.WithAttributes(
				KeyCrawlerHost.String(c.Host),
				KeyCrawlerDepthLimit.Int(c.DepthLimit),
				KeyCrawlerWait.String(c.Wait.String()),
			),
		)
		defer span.End()
		if err := msg.Ack(false); err != nil {
			c.Logger.WithError(err).Warn("could not send message acknowledgment")
		}
		atomic.AddUint64(&c.count, 1)

		var req web.PageRequest
		err := proto.Unmarshal(msg.Body, &req)
		if err != nil {
			c.Logger.WithError(err).Warn("could not unmarshal page request")
			span.AddEvent("Could not unmarshal page request")
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
			return nil // don't stop the crawler for an unmarshal error
		}
		span.SetAttributes(
			KeyPageRequestURL.String(req.URL),
			KeyPageRequestDepth.Int(int(req.Depth)),
			KeyPageRequestKey.String(req.HexKey()),
		)
		err = c.handle(ctx, &req)
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
		}
		return err
	}
}

func (c *Crawler) handle(ctx context.Context, req *web.PageRequest) error {
	span := trace.SpanFromContext(ctx)
	logger := c.Logger.WithFields(logrus.Fields{
		"url":   req.URL,
		"depth": req.Depth,
		"key":   hex.EncodeToString(req.Key),
	})
	logger.Debug("handling page request")

	u, err := url.Parse(req.URL)
	if err != nil {
		logger.WithError(err).Warn("could not parse url")
		span.RecordError(err)
		return nil // continue to next request
	}
	if c.shouldSkip(ctx, u) {
		return nil // continue to next request
	}
	if c.inURLSet(ctx, u) {
		return nil // continue to next request
	}

	page, err := c.Fetcher.Fetch(ctx, req)
	if err != nil {
		logger.WithError(err).Warn("failed to fetch page")
		span.RecordError(err, trace.WithAttributes(KeyLink.String(req.URL)))
		span.SetStatus(codes.Error, err.Error())
		return nil // continue to next request
	}

	// If the page was reached through a redirect, then we want to check if
	// we should crawl the final destination of the redirect.
	//
	// NOTE: The page.URL field should be the destination url if it was
	// redirected.
	if page.Redirected {
		span.AddEvent(EventRedirected)
		if c.shouldSkip(ctx, page.URL) {
			return nil
		}
		if c.inURLSet(ctx, page.URL) {
			return nil
		}
	}

	// copy the page data that we still need to prevent data races with visitor
	var (
		pageurl = *page.URL
		links   = make([]*url.URL, len(page.Links))
	)
	copy(links, page.Links)
	logger = logger.WithField("page_url", pageurl.String())

	// Yeet it over the fence
	go c.Visitor.Visit(ctx, page)

	// Check depth limit and skip publishing new links if limit reached
	if c.DepthLimit > 0 && page.Depth >= uint32(c.DepthLimit) {
		return nil
	}

	err = c.Publisher.Publish(logging.Stash(ctx, c.Logger), page.Depth, links)
	if err != nil {
		logger.WithError(err).Error("could not publish links")
		span.RecordError(err)
	}

	err = c.URLSet.Put(ctx, &pageurl)
	if err != nil {
		logger.WithError(err).Warn("could not store page_url in urlset")
		span.RecordError(err)
	}
	if page.Redirected {
		err = c.URLSet.Put(ctx, page.RedirectedFrom)
		if err != nil {
			logger.WithError(err).Warn("could not store redirected url in urlset")
			span.RecordError(err)
		}
	}
	return nil
}

func (c *Crawler) shouldSkip(ctx context.Context, u *url.URL) bool {
	if c.RobotsCtrl.ShouldSkip(u) {
		span := trace.SpanFromContext(ctx)
		span.AddEvent(
			EventInRobotsTXT,
			trace.WithAttributes(KeyLink.String(u.Redacted())),
		)
		return true
	}
	return false
}

func (c *Crawler) inURLSet(ctx context.Context, u *url.URL) bool {
	if c.URLSet.Has(ctx, u) {
		span := trace.SpanFromContext(ctx)
		span.AddEvent(
			EventInURLSet,
			trace.WithAttributes(KeyLink.String(u.Redacted())),
		)
		return true
	}
	return false
}

type timeoutFetcher struct {
	web.Fetcher
	Timeout time.Duration
}

func (tf *timeoutFetcher) Fetch(
	ctx context.Context,
	req *web.PageRequest,
) (*web.Page, error) {
	fetchCtx, cancelFetch := context.WithTimeout(ctx, tf.Timeout)
	page, err := tf.Fetcher.Fetch(fetchCtx, req)
	if err != nil {
		cancelFetch()
		return nil, err
	}
	cancelFetch()
	return page, nil
}
