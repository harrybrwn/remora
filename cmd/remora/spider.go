package main

import (
	"context"
	"fmt"
	"hash"
	"hash/fnv"
	"net/url"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/harrybrwn/remora/cmd"
	"github.com/harrybrwn/remora/event"
	"github.com/harrybrwn/remora/frontier"
	"github.com/harrybrwn/remora/internal/visitor"
	"github.com/harrybrwn/remora/storage"
	"github.com/harrybrwn/remora/web"
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
			Heartbeat: time.Minute * 5,
			Dial:      amqp.DefaultDial(30 * time.Second),
		},
	}
}

func newCrawlCmd(conf *cmd.Config) *cobra.Command {
	var (
		out   string
		sleep = time.Second * 1
	)
	c := cobra.Command{
		Use:   "crawl <url>",
		Short: "Small web crawler for local (non-distributed) crawls only",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			web.UserAgent = "Remora"
			u, err := url.Parse(args[0])
			if err != nil {
				return err
			}
			if u.Host == "" || u.Scheme == "" {
				return fmt.Errorf("bad url: %q", u.String())
			}
			var vis web.Visitor
			if out == "" {
				vis = &logVisitor{}
			} else {
				vis = visitor.NewFS(out, u.Host)
				if err = os.Mkdir(out, 0777); !os.IsExist(err) && err != nil {
					return err
				}
			}
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			bus := event.NewChannelBus()
			spider := NewSpider(
				vis,
				storage.NewInMemoryURLSet(),
				bus,
				withhost(u.Host),
				prefetch(1),
				wait(sleep),
				WithFetcher(web.NewFetcher(conf.UserAgent)),
			)
			go spider.start(ctx)
			go func() {
				time.Sleep(sleep * 2)
				r := web.NewPageRequest(u, 0)
				err = publish(bus, fnv.New128(), u.Host, r)
				if err != nil {
					fmt.Println(err)
					cancel()
					return
				}
				log.Infof("%s sent", u)
			}()
			go periodicMemLogs(ctx, sleep*10)
			<-ctx.Done()
			return ctx.Err()
		},
	}
	flags := c.Flags()
	flags.StringVarP(&out, "output", "o",
		out, "collect the crawl into an output directory")
	flags.DurationVarP(&sleep, "sleep", "s",
		sleep, "sleep time between page fetches")
	return &c
}

func newSpiderCmd(conf *cmd.Config) *cobra.Command {
	var (
		hosts    []string
		preFetch int
		sleep    time.Duration
		local    bool // TODO make this change the initial configuration
	)
	c := &cobra.Command{
		Use:   "spider <hostname(s)...>",
		Short: "Start a spider",
		RunE: func(cmd *cobra.Command, args []string) error {
			web.UserAgent = "Remora"
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
			vis, err := visitor.New(db, redis)
			if err != nil {
				return err
			}
			defer vis.Close()
			var (
				spiders = make([]*spider, len(hosts))
				front   = frontier.Frontier{
					RetryLimit: 30,
					Exchange:   exchange,
				}
			)
			err = front.Connect(ctx, connectConfig(conf))
			if err != nil {
				return errors.Wrap(err, "could not connect to frontier")
			}
			defer func() {
				front.Close()
				log.Info("spider: message queue connection closed")
				stop()
			}()
			opts := []spiderOpt{
				WithFetcher(web.NewFetcher(conf.UserAgent)),
				prefetch(conf.MessageQueue.Prefetch),
				limit(conf.Depth),
			}

			for i, host := range hosts {
				spiders[i] = &spider{
					Visitor: vis,
					URLSet:  storage.NewRedisURLSet(redis),
					Bus:     &front,
				}
				options := append(opts, wait(getWait(host, conf, cmd)), withhost(host))
				spiders[i].Init(options...)
			}
			for _, sp := range spiders {
				go sp.start(ctx)
			}
			go periodicMemLogs(ctx, time.Minute)
			<-ctx.Done()
			return nil
		},
	}
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

func NewSpider(
	visitor web.Visitor,
	urlset storage.URLSet,
	bus event.Bus,
	opts ...func(*spider),
) *spider {
	s := spider{Visitor: visitor, URLSet: urlset, Bus: bus}
	for _, o := range opts {
		o(&s)
	}
	return &s
}

type spiderOpt func(*spider)

func prefetch(n int) func(*spider)            { return func(s *spider) { s.Prefetch = n } }
func limit(n uint) func(*spider)              { return func(s *spider) { s.Limit = n } }
func wait(d time.Duration) func(*spider)      { return func(s *spider) { s.Wait = d } }
func withhost(h string) func(*spider)         { return func(s *spider) { s.Host = h } }
func WithHash(h hash.Hash) func(*spider)      { return func(s *spider) { s.hash = h } }
func WithFetcher(f web.Fetcher) func(*spider) { return func(s *spider) { s.Fetcher = f } }
func WithVisitor(v web.Visitor) spiderOpt     { return func(s *spider) { s.Visitor = v } }
func WithURLSet(u storage.URLSet) spiderOpt   { return func(s *spider) { s.URLSet = u } }
func WIthBus(b event.Bus) spiderOpt           { return func(s *spider) { s.Bus = b } }

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
	Fetcher web.Fetcher

	sleep  chan time.Duration
	robots *robotstxt.RobotsData
	mu     sync.Mutex
	hash   hash.Hash
}

func (s *spider) Init(opts ...spiderOpt) {
	for _, o := range opts {
		o(s)
	}
}

// Initialize with defaults
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
	if s.Fetcher == nil {
		s.Fetcher = web.NewFetcher("Remora")
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
	if s.Host == "" {
		return errors.New("no hostname to crawl")
	}
	s.robots, err = web.GetRobotsTxT(s.Host)
	if err != nil {
		log.WithError(err).Error("could not get robots.txt")
		return err
	}
	s.init()

	log.WithFields(logrus.Fields{
		"wait": s.Wait, "host": s.Host}).Info("starting spider")
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
		event.WithPrefetch(s.Prefetch),
		event.WithKeys(s.hostHashKey()),
		frontier.WithAutoAck(false), // for rabbitmq message busses
	)
	// Catch errors with options
	if errors.Is(err, event.ErrWrongConsumerType) {
		log.WithError(err).Warn("could not configure consumer correctly")
	} else if err != nil {
		return err
	}
	defer consumer.Close()

	deliveries, err := consumer.Consume()
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
				"key": msg.RoutingKey, "tag": msg.DeliveryTag,
				"exchange": msg.Exchange, "type": msg.Type,
				"consumer": msg.ConsumerTag, "depth": req.Depth,
				"url": req.URL,
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
		page  *web.Page
		err   error
	)
	page = web.NewPageFromString(req.URL, req.Depth)
	if page == nil {
		err = errors.New("could not parse page request url")
		log.WithFields(logrus.Fields{
			"url":   req.URL,
			"error": err,
		}).Error("could not get page from page request")
		go func() { s.sleep <- s.Wait }()
		return err
	}
	s.Visitor.LinkFound(page.URL)

	if s.inRobotsTxt(page.URL.Path) || s.URLSet.Has(page.URL) {
		log.WithField(
			"url", req.URL,
		).Trace("already seen or found in robots.txt")
		go func() { s.sleep <- s.Wait }()
		return nil
	}

	// Assume that if handleRequest is called then it made a
	// request to the web server and so we need to wait for politness.
	fetchCtx, cancel := context.WithTimeout(ctx, time.Minute)
	page, err = s.Fetcher.Fetch(fetchCtx, req)
	if err != nil {
		log.WithError(err).Error("could not fetch page")
		cancel()
		go func() { s.sleep <- 0 }()
		return nil
	}
	cancel()
	go func() { s.sleep <- time.Since(start) }()

	if page.Redirected {
		if s.inRobotsTxt(page.URL.Path) || s.URLSet.Has(page.URL) {
			return nil
		}
	}

	s.Visitor.Visit(ctx, page)
	err = s.publishLinks(ctx, pub, page)
	if err != nil {
		log.WithError(err).Error("failed to publish page")
	}

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
				"error": err, "url": page.URL,
			}).Warn("could mark redirected url as seen")
		}
	}
	return nil
}

func (s *spider) inRobotsTxt(path string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, agent := range []string{
		"*",
		web.UserAgent,
	} {
		if !s.robots.TestAgent(path, agent) {
			return true
		}
	}
	return false
}

func (s *spider) publishLinks(
	ctx context.Context,
	pub event.Publisher,
	page *web.Page,
) error {
	var (
		err      error
		done     = ctx.Done()
		haslinks = len(page.Links) > 0
	)

	// Only push the page's links if those pages will
	// be crawled in the future. If the page's links are
	// going to exceed the depth limit then they will be
	// discarded in the future so we should not push them
	// to the queue now.
	if !haslinks {
		return nil
	}
	if s.Limit != 0 && page.Depth >= uint32(s.Limit) {
		return nil
	}

	visited := s.URLSet.HasMulti(page.Links)
	for i, l := range page.Links {
		select {
		case <-done:
			return nil
		default:
		}
		if l.Host == s.Host && s.inRobotsTxt(l.Path) {
			continue
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
		err = publish(pub, s.hash, l.Host, r)
		if err != nil && !errors.Is(err, event.ErrNoQueue) {
			log.WithError(err).Error("could not publish message")
			continue
		}
	}
	return nil
}

func (s *spider) hostHashKey() string {
	s.hash.Reset()
	s.hash.Write([]byte(s.Host))
	key := fmt.Sprintf("%x.%s", s.hash.Sum(nil)[:2], s.Host)
	s.hash.Reset()
	return key
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

var _ web.Visitor = (*logVisitor)(nil)
