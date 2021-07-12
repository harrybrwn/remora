package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/go-redis/redis"
	"github.com/harrybrwn/diktyo/frontier"
	"github.com/harrybrwn/diktyo/storage"
	"github.com/harrybrwn/diktyo/web"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/streadway/amqp"
	"github.com/temoto/robotstxt"
	"google.golang.org/protobuf/proto"
)

type crawler struct {
	host  string
	limit uint
	wait  time.Duration
	v     web.Visitor

	conn      *amqp.Connection
	chMu      sync.Mutex    // amqp channel mutex for multithreaded publishing
	publisher *amqp.Channel // this channel if for publishing only
	sleep     chan time.Duration

	urlset storage.URLSet
	mu     sync.Mutex
	robots *robotstxt.RobotsData

	Prefetch int
}

func newCrawler(host string, vis web.Visitor, conn *amqp.Connection, redis *redis.Client, conf *Config) (*crawler, error) {
	var err error
	c := &crawler{
		host:     host,
		limit:    conf.Depth,
		wait:     getWait(host, &conf.Config),
		sleep:    make(chan time.Duration),
		v:        vis,
		conn:     conn,
		urlset:   storage.NewRedisURLSet(redis),
		Prefetch: conf.MessageQueue.Prefetch,
	}

	c.publisher, err = c.conn.Channel()
	if err != nil {
		c.conn.Close()
		return nil, errors.Wrap(err, "could not open amqp channel")
	}
	log.Trace("message queue channel open")

	c.robots, err = web.GetRobotsTxT(c.host)
	if err != nil {
		log.WithError(err).Warn("could not open robots.txt")
	} else {
		log.Trace("retrieved robots.txt")
	}
	if c.wait == 0 {
		c.wait = 1 // 1 microsecond bc timers fail with 0
	}
	return c, nil
}

func (c *crawler) Close() error {
	err := c.publisher.Close()
	if err != nil {
		return err
	}
	return nil
}

func (c *crawler) listen(ctx context.Context) error {
	var (
		err        error
		connClose  = make(chan *amqp.Error)
		chanClose  = make(chan *amqp.Error)
		chanCancel = make(chan string)
		blocked    = make(chan amqp.Blocking)
	)

	c.conn.NotifyClose(connClose)
	c.conn.NotifyBlocked(blocked)

	ch, err := c.conn.Channel()
	if err != nil {
		log.WithError(err).Error("could not create channel for consuming")
		return err
	}
	defer ch.Close()

	ch.NotifyClose(chanClose)
	ch.NotifyCancel(chanCancel)

	q, err := frontier.DeclareHostQueue(ch, c.host)
	if err != nil {
		return err
	}
	err = frontier.DeclarePageExchange(c.publisher)
	if err != nil {
		log.WithError(err).Error("could not declare exchange with publisher channel")
		return err
	}
	for _, k := range []string{q.Name, "any"} {
		err = ch.QueueBind(q.Name, k, frontier.PageExchangeName, false, nil)
		if err != nil {
			log.WithError(err).Errorf("could not bind queue to %s", k)
			continue
		}
	}

	if c.Prefetch == 0 {
		c.Prefetch = 1
	}
	err = ch.Qos(c.Prefetch, 0, false)
	if err != nil {
		return err
	}
	delivery, err := ch.Consume(q.Name, "", false, false, false, false, nil)
	if err != nil {
		return err
	}

	timer := time.NewTimer(0)
	defer timer.Stop()

	log.Infof("checking message queue every %v", c.wait)
	for {
		select {
		case <-ctx.Done():
			log.Info("stopping crawler")
			return nil
		case tm := <-c.sleep:
			tm = c.wait - tm
			if tm < 0 {
				break
			}
			timer.Reset(tm)
			select {
			case <-timer.C:
			case <-ctx.Done():
				return nil
			}
		case msg := <-delivery:
			log.WithFields(logrus.Fields{
				"key": msg.RoutingKey, "tag": msg.DeliveryTag,
				"exchange": msg.Exchange, "type": msg.Type,
				"consumer": msg.ConsumerTag,
			}).Trace("message")
			go c.handleMsg(ctx, &msg)
		case e := <-connClose:
			log.WithError(e).Warning("connection closed")
			return e
		case e := <-chanClose:
			log.WithError(e).Warning("channel closed")
			return e
		case b := <-blocked:
			log.WithField("reason", b.Reason).Warning("connection blocked")
			return fmt.Errorf("connection blocked: %v", b.Reason)
		case tag := <-chanCancel:
			log.WithField("tag", tag).Info("received a cancel event")
			return nil
		}
	}
}

func (c *crawler) Pause(dur time.Duration) { c.sleep <- dur }

var hostname string

func init() {
	var err error
	hostname, err = os.Hostname()
	if err != nil {
		log.Warn("could not get hostname")
	}
}

func (c *crawler) handleMsg(ctx context.Context, msg *amqp.Delivery) error {
	var (
		err   error
		req   = new(web.PageRequest)
		start = time.Now()
	)

	msgType := frontier.ParseMessageType(msg.Type)
	if msgType != frontier.PageRequest {
		// This message was not meant for use, ignore it and don't ack
		log.Warn("skipping message: not a page request message")
	}

	defer func() {
		// only ack if the context is not canceled
		select {
		case <-ctx.Done():
			return
		default:
			msg.Ack(false)
		}
	}()

	if len(msg.Body) == 0 {
		log.WithFields(logrus.Fields{
			"hostname": hostname,
			"host":     c.host,
		}).Error("received zero length body")
		return nil
	}

	err = proto.Unmarshal(msg.Body, req)
	if err != nil {
		log.WithError(err).Error("could not unmarshal page request")
		return err
	}
	page := web.NewPageFromString(req.URL, req.Depth)

	c.mu.Lock()
	test := c.robots.TestAgent(page.URL.Path, "*")
	c.mu.Unlock()
	if !test {
		log.WithField("url", req.URL).Info("found in robots.txt")
		return nil
	}
	if c.urlset.Has(page.URL) {
		log.WithField("url", req.URL).Trace("skipping request")
		return nil
	}

	c.v.LinkFound(page.URL)
	// Assume that if handleRequest is called then it made a
	// request to the web server and so we need to wait for politness.
	defer func() { c.sleep <- time.Since(start) }()
	err = c.handleRequest(ctx, page)
	if err != nil {
		log.WithError(err).Error("error while handling request")
		return nil
	}
	return nil
}

func (c *crawler) handleRequest(ctx context.Context, page *web.Page) error {
	fetchCtx, cancel := context.WithTimeout(ctx, time.Minute)
	err := page.FetchCtx(fetchCtx)
	if err != nil {
		cancel()
		return errors.Wrap(err, "could not fetch page")
	}
	cancel()

	// If the page was redirected then the main url on the
	// page struct changes to the redirect location (which
	// is a pretty leaky abstraction).
	// If we have seen the redirected url then skip it
	if page.Redirected && c.urlset.Has(page.URL) {
		return nil
	}

	var host = c.host
	c.v.Visit(ctx, page)

	haslinks := len(page.Links) > 0
	// Only push the page's links if those pages will
	// be crawled in the future. If the page's links are
	// going to exceed the depth limit then they will be
	// discarded in the future so we should not push them
	// to the queue now.
	if haslinks && (c.limit == 0 || page.Depth+1 < uint32(c.limit)) {
		visited := c.urlset.HasMulti(page.Links)
		log.Info("queuing up links")
		done := ctx.Done()
		for i, l := range page.Links {
			select {
			case <-done:
				continue
			default:
			}
			if l.Host == host {
				c.mu.Lock()
				test := c.robots.TestAgent(l.Path, "*")
				c.mu.Unlock()
				if !test {
					continue
				}
			}
			if visited[i] {
				continue
			}

			r := web.NewPageRequest(l, page.Depth+1)
			select {
			case <-done:
				continue
			default:
			}
			c.chMu.Lock()
			err = frontier.PushRequestToHost(c.publisher, r, l.Host)
			c.chMu.Unlock()
			if err != nil {
				log.WithError(err).Error("could not publish discovered link")
				continue
			}
		}
	}
	err = c.urlset.Put(page.URL)
	if err != nil {
		log.WithError(err).WithField("url", page.URL).Warn("could not mark as seen")
		return err
	}
	if page.Redirected {
		err = c.urlset.Put(page.RedirectedFrom)
		if err != nil {
			log.WithError(err).WithField("ur", page.URL).Warn("could mark redirected url as seen")
			return err
		}
	}
	return nil
}

func (c *crawler) depthLimitReached(d uint32) bool {
	if c.limit == 0 {
		return false
	}
	return d >= uint32(c.limit)
}

func (c *crawler) shouldSkip(page *web.Page) bool {
	return c.shouldSkipURL(page.URL, uint(page.Depth))
}

func (c *crawler) shouldSkipURL(u *url.URL, depth uint) bool {
	if c.depthLimitReached(uint32(depth)) {
		return true
	}
	c.mu.Lock()
	test := c.robots.TestAgent(u.Path, "*")
	c.mu.Unlock()
	if !test {
		log.WithField("url", u.String()).Trace("url path is in robots.txt")
		return true
	}
	return c.urlset.Has(u)
}
