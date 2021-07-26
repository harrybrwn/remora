package main

import (
	"context"
	"database/sql"
	"fmt"
	"hash"
	"hash/fnv"
	"net/url"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/go-redis/redis"
	"github.com/harrybrwn/diktyo/cmd"
	"github.com/harrybrwn/diktyo/db"
	"github.com/harrybrwn/diktyo/event"
	"github.com/harrybrwn/diktyo/frontier"
	"github.com/harrybrwn/diktyo/internal/visitor"
	"github.com/harrybrwn/diktyo/storage"
	"github.com/harrybrwn/diktyo/web"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
	"github.com/streadway/amqp"
	"github.com/temoto/robotstxt"
	"google.golang.org/protobuf/proto"
)

var (
	exchange = frontier.Exchange{
		Name:    frontier.PageExchangeName,
		Kind:    "topic",
		Durable: true,
	}
)

func connectConfig(conf *cmd.Config) frontier.Connect {
	return frontier.Connect{
		Host: conf.MessageQueue.Host,
		Port: conf.MessageQueue.Port,
		Config: &amqp.Config{
			Locale:    "en_US",
			Heartbeat: time.Minute * 2,
			Dial:      amqp.DefaultDial(30 * time.Second),
		},
	}
}

func newSpiderCmd(conf *cmd.Config) *cobra.Command {
	var (
		hosts    []string
		nop      bool
		preFetch int
		sleep    time.Duration
		local    bool // TODO make this change the initial configuration
	)
	c := &cobra.Command{
		Use: "spider", Short: "Start a spider",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(
				cmd.Context(),
				os.Interrupt,
				syscall.SIGTERM,
			)
			defer stop()
			hosts = append(hosts, args...)
			if len(hosts) == 0 {
				return errors.New("no hosts to crawl with spider")
			}
			if cmd.Flags().Lookup("prefetch").Changed {
				conf.MessageQueue.Prefetch = preFetch
			}
			if cmd.Flags().Lookup("sleep").Changed {
				conf.Sleep = sleep
			}
			web.HttpClient.Timeout = time.Minute

			db, redis, err := getDataStores(conf)
			if err != nil {
				return errors.Wrap(err, "datastore connection failure")
			}
			log.Info("connected to datastores")
			defer func() {
				db.Close()
				redis.Close()
				log.Info("remote connections closed")
			}()
			var (
				spiders = make([]*spider, len(hosts))
				vis     web.Visitor
				front   = frontier.Frontier{RetryLimit: 30, Exchange: exchange}
			)
			if nop {
				vis = &web.NoOpVisitor{}
				if err = db.Close(); err != nil {
					log.WithError(err).Warn("couldn't close database")
				}
			} else {
				vis = visitor.New(db, redis)
			}
			err = front.Connect(ctx, connectConfig(conf))
			if err != nil {
				return errors.Wrap(err, "could not connect to frontier")
			}
			defer func() {
				front.Close()
				log.Info("spider: message queue connection closed")
				stop()
			}()

			for i, host := range hosts {
				spiders[i] = NewSpider(
					vis,
					storage.NewRedisURLSet(redis),
					&front,
					prefetch(conf.MessageQueue.Prefetch),
					limit(conf.Depth),
					wait(getWait(host, conf, cmd)),
					withhost(host),
				)
			}
			for _, sp := range spiders {
				go sp.start(ctx)
			}
			go periodicMemLogs(ctx, time.Second*15)
			<-ctx.Done()
			return nil
		},
	}
	c.Flags().BoolVar(&nop, "nop", nop, "no operation when visiting a page")
	c.Flags().StringArrayVar(&hosts, "host",
		hosts, "host of website being crawled")
	c.Flags().DurationVar(&sleep, "sleep", sleep, "")
	c.Flags().IntVar(
		&preFetch, "prefetch",
		conf.MessageQueue.Prefetch,
		"number of messages prefetched from the message queue")
	c.Flags().BoolVar(&local, "local", local, "run the crawler locally only")
	return c
}

func getDataStores(conf *cmd.Config) (d *sql.DB, r *redis.Client, err error) {
	d, err = db.New(&conf.DB)
	if err != nil {
		err = errors.Wrap(err, "could not connect to sql database")
		return
	}
	r = redis.NewClient(conf.RedisOpts())
	if err = r.Ping().Err(); err != nil {
		d.Close()
		err = errors.Wrap(err, "could not ping redis server")
		return
	}
	return
}

func NewSpider(
	visitor web.Visitor,
	urlset storage.URLSet,
	bus event.Bus,
	opts ...func(*spider),
) *spider {
	s := spider{
		Visitor: visitor,
		URLSet:  urlset,
		Bus:     bus,
	}
	for _, o := range opts {
		o(&s)
	}
	return &s
}

func prefetch(n int) func(*spider)       { return func(s *spider) { s.Prefetch = n } }
func limit(n uint) func(*spider)         { return func(s *spider) { s.Limit = n } }
func wait(d time.Duration) func(*spider) { return func(s *spider) { s.Wait = d } }
func withhost(h string) func(*spider)    { return func(s *spider) { s.Host = h } }
func WithHash(h hash.Hash) func(*spider) { return func(s *spider) { s.hash = h } }

type spider struct {
	// Settings
	Prefetch int
	Limit    uint
	Wait     time.Duration
	Host     string
	// Hooks
	Visitor web.Visitor
	URLSet  storage.URLSet
	Bus     event.Bus

	sleep  chan time.Duration
	robots *robotstxt.RobotsData
	mu     sync.Mutex
	hash   hash.Hash
}

func (s *spider) init() {
	s.sleep = make(chan time.Duration)
	if s.Host == "" {
		log.Error("spider has no host")
		return
	}
	if s.URLSet == nil {
		log.Warn("spider has no URL set")
		s.URLSet = &noOpURLSet{}
	}
	if s.Bus == nil {
		log.Warn("spider has no event bus")
		s.Bus = &noOpEventBus{ch: make(chan amqp.Delivery)}
	}
	if s.robots == nil {
		s.robots = web.AllowAll()
	}
	if s.Visitor == nil {
		s.Visitor = &web.NoOpVisitor{}
	}
	if s.Wait == 0 {
		s.Wait = 1
	}
	if s.Prefetch == 0 {
		s.Prefetch = 1
	}
	s.hash = fnv.New128()
}

func (s *spider) start(ctx context.Context) (err error) {
	s.robots, err = web.GetRobotsTxT(s.Host)
	if err != nil {
		log.WithError(err).Error("could not get robots.txt")
		return err
	}
	s.init()

	log.WithFields(logrus.Fields{
		"wait": s.Wait,
		"host": s.Host,
	}).Info("starting spider")
	publisher, err := s.Bus.Publisher(
		event.PublishWithContext(ctx),
	)
	if err != nil {
		log.WithError(err).Error("could not create publisher")
		return err
	}
	consumer, err := s.Bus.Consumer(
		s.Host,
		event.ConsumeWithContext(ctx),
		frontier.WithAutoAck(false),
		frontier.WithPrefetch(s.Prefetch),
	)
	if err == frontier.ErrWrongConsumerType {
		log.WithError(err).Warn("could not configure consumer correctly")
	} else if err != nil {
		return err
	}
	defer consumer.Close()

	deliveries, err := consumer.Consume(s.Host, fmt.Sprintf("*.%s", s.Host))
	if err != nil {
		return err
	}
	defer log.WithField("host", s.Host).Info("stopping spider")

	for {
		select {
		case <-ctx.Done():
			return nil
		case tm := <-s.sleep:
			tm = s.Wait - tm
			if tm <= 0 {
				break
			}
			log.Trace("sleeping for ", tm)
			time.Sleep(tm)
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
				"key":      msg.RoutingKey,
				"tag":      msg.DeliveryTag,
				"exchange": msg.Exchange,
				"type":     msg.Type,
				"consumer": msg.ConsumerTag,
				"depth":    req.Depth,
				"url":      req.URL,
			}).Trace("request")
			go func() {
				err = s.handle(ctx, &req, publisher)
				if err == nil {
					if err = msg.Ack(false); err != nil {
						log.WithFields(logrus.Fields{
							"error": err,
						}).Warning("could not acknowledge message")
					}
				}
			}()
		}
	}
}

func (s *spider) handle(
	ctx context.Context,
	req *web.PageRequest,
	pub event.Publisher,
) error {
	var (
		start = time.Now()
		page  = web.NewPageFromString(req.URL, req.Depth)
	)
	if page == nil {
		err := errors.New("could not parse page request url")
		log.WithFields(logrus.Fields{
			"url":   req.URL,
			"error": err,
		}).Error("could not get page from page request")
		go func() { s.sleep <- s.Wait }()
		return err
	}
	s.Visitor.LinkFound(page.URL)

	s.mu.Lock()
	ok := s.robots.TestAgent(page.URL.Path, "*")
	s.mu.Unlock()
	if !ok {
		log.WithField("url", req.URL).Trace("found in robots.txt")
		go func() { s.sleep <- s.Wait }()
		return nil
	}
	if s.URLSet.Has(page.URL) {
		log.WithField("url", req.URL).Trace("already seen")
		go func() { s.sleep <- s.Wait }()
		return nil
	}

	// Assume that if handleRequest is called then it made a
	// request to the web server and so we need to wait for politness.
	fetchCtx, cancel := context.WithTimeout(ctx, time.Minute)
	err := page.FetchCtx(fetchCtx)
	if err != nil {
		log.WithError(err).Error("could not fetch page")
		cancel()
		go func() { s.sleep <- 0 }()
		return nil
	}
	cancel()
	go func() { s.sleep <- time.Since(start) }()

	if page.Redirected {
		s.mu.Lock()
		ok = s.robots.TestAgent(page.URL.Path, "*")
		s.mu.Unlock()
		if ok || s.URLSet.Has(page.URL) {
			return nil
		}
	}

	err = s.publishLinks(ctx, pub, page)
	if err != nil {
		log.WithError(err).Error("failed to publish page")
	}

	s.Visitor.Visit(ctx, page)

	err = s.URLSet.Put(page.URL)
	if err != nil {
		log.WithFields(logrus.Fields{
			"error": err, "url": page.URL,
		}).Warn("could not mark as seen")
	}
	if page.Redirected {
		err = s.URLSet.Put(page.RedirectedFrom)
		if err != nil {
			log.WithFields(logrus.Fields{
				"error": err,
				"url":   page.URL,
			}).Warn("could mark redirected url as seen")
		}
	}
	return nil
}

func (s *spider) publishLinks(
	ctx context.Context,
	pub event.Publisher,
	page *web.Page,
) error {
	var (
		err      error
		ok       bool
		haslinks = len(page.Links) > 0
	)

	// Only push the page's links if those pages will
	// be crawled in the future. If the page's links are
	// going to exceed the depth limit then they will be
	// discarded in the future so we should not push them
	// to the queue now.
	if haslinks && (s.Limit == 0 || page.Depth < uint32(s.Limit)) {
		visited := s.URLSet.HasMulti(page.Links)
		done := ctx.Done()
		for i, l := range page.Links {
			select {
			case <-done:
				return nil
			default:
			}
			if l.Host == s.Host {
				s.mu.Lock()
				ok = s.robots.TestAgent(l.Path, "*")
				s.mu.Unlock()
				if !ok {
					continue
				}
			}
			if visited[i] {
				continue
			}
			switch l.Scheme {
			case
				"javascript", // "javascript:print();" or "javascript:void(0);" links
				"mailto",     // skip email addresses
				"tel",        // skip phone numbers
				"":
				continue
			default:
			}

			r := web.NewPageRequest(l, page.Depth+1)

			select {
			case <-done:
				return nil
			default:
			}
			err = publish(pub, s.hash, l.Host, r)
			if err != nil {
				log.WithError(err).Error("could not publish message")
				continue
			}
		}
	}
	return nil
}

func getWait(
	host string,
	conf *cmd.Config,
	cmd *cobra.Command,
) time.Duration {
	var tm time.Duration
	f := cmd.Flags().Lookup("sleep")
	if f.Changed {
		return conf.Sleep
	}
	t, ok := conf.WaitTimes[host]
	if ok {
		var err error
		tm, err = time.ParseDuration(t)
		if err != nil {
			tm = conf.Sleep
		}
	} else {
		tm = conf.Sleep
	}
	if tm == 0 {
		tm = 1
	}
	return tm
}

type noOpURLSet struct{}

func (*noOpURLSet) Put(*url.URL) error           { return nil }
func (*noOpURLSet) Has(*url.URL) bool            { return false }
func (*noOpURLSet) HasMulti(u []*url.URL) []bool { return make([]bool, len(u)) }

type noOpEventBus struct {
	ch chan amqp.Delivery
}

func (b *noOpEventBus) Close() error {
	if b.ch != nil {
		close(b.ch)
	}
	return nil
}
func (b *noOpEventBus) Consumer(string, ...event.ConsumerOpt) (event.Consumer, error) {
	if b.ch == nil {
		b.ch = make(chan amqp.Delivery)
	}
	return b, nil
}
func (b *noOpEventBus) Consume(...string) (<-chan amqp.Delivery, error) {
	return b.ch, nil
}
func (b *noOpEventBus) Publisher(...event.PublisherOpt) (event.Publisher, error) { return b, nil }
func (b *noOpEventBus) Publish(string, amqp.Publishing) error                    { return nil }
func (b *noOpEventBus) WithOpt(...event.ConsumerOpt) error                       { return nil }
