package main

import (
	"context"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/go-redis/redis"
	"github.com/harrybrwn/diktyo/frontier"
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

	conn *amqp.Connection
	chMu sync.Mutex    // amqp channel mutex for multithreaded publishing
	ch   *amqp.Channel // this channel if for publishing only

	visited *redis.Client
	mu      sync.Mutex
	robots  *robotstxt.RobotsData

	// Purge the visited set when the crawler is closed
	PurgeVisited bool
	Prefetch     int
}

func newCrawler(conf *Config, vis web.Visitor, conn *amqp.Connection) (*crawler, error) {
	var err error
	c := &crawler{
		host:  conf.Host,
		limit: conf.Depth,
		wait:  getWait(conf.Host, &conf.Config),
		v:     vis,
		conn:  conn,
	}

	c.ch, err = c.conn.Channel()
	if err != nil {
		c.conn.Close()
		return nil, errors.Wrap(err, "could not open amqp channel")
	}
	log.Trace("message queue channel open")
	c.visited = redis.NewClient(conf.RedisOpts())

	c.robots, err = web.GetRobotsTxT(c.host)
	if err != nil {
		log.WithError(err).Warn("could not open robots.txt")
	} else {
		log.Trace("retrieved robots.txt")
	}
	if c.wait == 0 {
		c.wait = 1 // 1 microsecond
	}
	return c, c.visited.Ping().Err()
}

func (c *crawler) Close() error {
	var err error
	if c.PurgeVisited {
		c.visited.FlushDB().Err()
	}
	err = c.ch.Close()
	if err != nil {
		return err
	}
	return c.visited.Close()
}

func (c *crawler) listen(ctx context.Context) error {
	var (
		err        error
		connClose  = make(chan *amqp.Error)
		chanClose  = make(chan *amqp.Error)
		chanCancel = make(chan string)
		blocked    = make(chan amqp.Blocking)
	)
	c.visited = c.visited.WithContext(ctx)

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

	const exchange = "page"
	err = ch.ExchangeDeclare(exchange, "topic", true, false, false, false, nil)
	if err != nil {
		log.WithError(err).Error("could not declare exchange")
		return err
	}
	err = c.ch.ExchangeDeclare(exchange, "topic", true, false, false, false, nil)
	if err != nil {
		log.WithError(err).Error("could not declare exchange with publisher channel")
		return err
	}

	q, err := frontier.DeclareHostQueue(ch, c.host)
	if err != nil {
		return err
	}

	if c.Prefetch == 0 {
		c.Prefetch = 1
	}
	err = ch.Qos(c.Prefetch, 0, false)
	if err != nil {
		return err
	}
	for _, k := range []string{q.Name, "any"} {
		err = ch.QueueBind(q.Name, k, exchange, false, nil)
		if err != nil {
			log.WithError(err).Errorf("could not bind queue to %s", k)
			continue
		}
	}
	delivery, err := ch.Consume(
		q.Name,
		"",
		false,
		false,
		false,
		false,
		nil,
	)
	if err != nil {
		return err
	}

	tick := time.NewTicker(c.wait)
	defer tick.Stop()

	log.Infof("checking message queue every %v", c.wait)
	for {
		select {
		case msg := <-delivery:
			log.WithFields(logrus.Fields{
				"key":      msg.RoutingKey,
				"tag":      msg.DeliveryTag,
				"exchange": msg.Exchange,
			}).Info("message")
			go c.handleMsg(ctx, &msg)
			select {
			case <-ctx.Done():
				return nil
			case <-tick.C:
				tick.Reset(c.wait)
			}
		case <-ctx.Done():
			log.Info("stopping crawler")
			return nil
		case <-connClose:
			log.Warning("connection closed")
			return nil
		case <-chanClose:
			log.Warning("channel closed")
			return nil
		case b := <-blocked:
			log.WithField("reason", b.Reason).Warning("connection blocked")
		case tag := <-chanCancel:
			log.WithField("tag", tag).Info("received a cancel event")
			return nil
		}
	}
}

var errSkippedPage = errors.New("skipped page")
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
		err error
		req = new(web.PageRequest)
	)
	start := time.Now()
	defer func() {
		log.Infof("handled message in %v", time.Since(start))
	}()
	defer func() { msg.Ack(false) }()
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
	c.v.LinkFound(page.URL)

	// if c.shouldSkip(page) {
	// 	log.WithFields(logrus.Fields{
	// 		"url":   page.URL.String(),
	// 		"depth": page.Depth,
	// 		"limit": c.limit,
	// 	}).Trace("skipping page")
	// 	return errSkippedPage
	// }

	err = c.handleRequest(ctx, page)
	if err != nil {
		log.WithError(err).Error("error while handling request")
		return nil
	}

	err = c.markSeen(*page.URL)
	if err != nil {
		log.WithError(err).WithField("url", req.URL).Warn("could not mark as seen")
	}
	if page.Redirected {
		err = c.markSeen(*page.RedirectedFrom)
		if err != nil {
			log.WithError(err).Warn("could mark redirected url as seen")
		}
	}
	return nil
}

func (c *crawler) handleRequest(ctx context.Context, page *web.Page) error {
	err := page.FetchCtx(ctx)
	if err != nil {
		return errors.Wrap(err, "could not fetch page")
	}
	host := c.host

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		c.v.Visit(ctx, page)
		wg.Done()
	}()

	haslinks := len(page.Links) > 0
	// Only push the page's links if those pages will
	// be crawled in the future. If the page's links are
	// going to exceed the depth limit then they will be
	// discarded in the future so we should not push them
	// to the queue now.
	if haslinks && (c.limit == 0 || page.Depth+1 < uint32(c.limit)) {
		var mu sync.Mutex
		keys := urlKeys(page.Links)
		c.mu.Lock()
		visited, err := c.visited.MGet(keys...).Result()
		c.mu.Unlock()
		if err != nil {
			log.WithFields(logrus.Fields{
				"error":   err,
				"n-keys":  len(keys),
				"n-links": len(page.Links),
			}).Error("could not access set of visited urls")
		}

		for i, l := range page.Links {
			wg.Add(1)
			go func(ix int, l *url.URL) {
				defer wg.Done()
				if l.Host == host {
					c.mu.Lock()
					test := c.robots.TestAgent(l.Path, "*")
					c.mu.Unlock()
					if !test {
						return
					}
				}

				mu.Lock()
				if visited[ix] != nil {
					mu.Unlock()
					return
				}
				mu.Unlock()

				r := web.NewPageRequest(l, page.Depth+1)
				c.chMu.Lock()
				err = frontier.PushRequestToHost(c.ch, r, l.Host)
				c.chMu.Unlock()
				if err != nil {
					log.WithError(err).Error("could not publish discovered link")
					return
				}
			}(i, l)
		}
	}
	wg.Wait()
	return nil
}

func urlKeys(links []*url.URL) []string {
	s := make([]string, len(links))
	var u url.URL
	for i, l := range links {
		u = *l
		u.Fragment = ""
		u.RawFragment = ""
		s[i] = u.String()
	}
	return s
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
	return c.seen(*u)
}

func (c *crawler) seen(u url.URL) bool {
	u.Fragment = ""
	u.RawFragment = ""
	err := c.visited.Get(u.String()).Err()
	return err == nil
}

func (c *crawler) markSeen(u url.URL) error {
	u.Fragment = ""
	u.RawFragment = ""
	return c.visited.Set(u.String(), 1, 0).Err()
}
