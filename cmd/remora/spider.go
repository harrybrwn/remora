package main

import (
	"context"
	"database/sql"
	"fmt"
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

func newSpiderCmd(conf *cmd.Config) *cobra.Command {
	var (
		hosts    []string
		nop      bool
		prefetch int
		sleep    time.Duration
	)
	c := &cobra.Command{
		Use: "spider", Short: "Start a spider",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			hosts = append(hosts, args...)
			if len(hosts) == 0 {
				return errors.New("no hosts to crawl with spider")
			}
			if cmd.Flags().Lookup("prefetch").Changed {
				conf.MessageQueue.Prefetch = prefetch
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
			var (
				spiders = make([]*spider, len(hosts))
				vis     web.Visitor
				front   = frontier.Frontier{Exchange: frontier.Exchange{
					Name:    frontier.PageExchangeName,
					Kind:    "topic",
					Durable: true,
				}}
			)
			if nop {
				vis = &web.NoOpVisitor{}
				if err = db.Close(); err != nil {
					log.WithError(err).Warn("couldn't close database")
				}
			} else {
				vis = visitor.New(db)
			}
			err = front.Connect(frontier.Connect{
				Host: conf.MessageQueue.Host,
				Port: conf.MessageQueue.Port,
				Config: &amqp.Config{
					// Heartbeat: time.Millisecond,
					Heartbeat: time.Second * 10,
					Locale:    "en_US",
				},
			})
			if err != nil {
				return errors.Wrap(err, "could not connect to frontier")
			}
			defer func() {
				front.Close()
				log.Info("spider: message queue connection closed")
				stop()
			}()

			for i, host := range hosts {
				spiders[i] = &spider{
					Host:     host,
					Prefetch: conf.MessageQueue.Prefetch,
					Limit:    conf.Depth,
					Wait:     getWait(host, conf, cmd),
					Bus:      &front,
					URLSet:   storage.NewRedisURLSet(redis),
					Visitor:  vis,
				}
			}
			for _, sp := range spiders {
				go sp.start(ctx)
			}
			<-ctx.Done()
			return nil
		},
	}
	c.Flags().BoolVar(&nop, "nop", nop, "no operation when visiting a page")
	c.Flags().StringArrayVar(&hosts, "host", hosts, "host of website being crawled")
	c.Flags().DurationVar(&sleep, "sleep", sleep, "")
	c.Flags().IntVar(
		&prefetch, "prefetch",
		conf.MessageQueue.Prefetch,
		"number of messages prefetched from the message queue")
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

type spider struct {
	sleep chan time.Duration

	robots *robotstxt.RobotsData
	mu     sync.Mutex

	// Settings
	Prefetch int
	Limit    uint
	Wait     time.Duration
	Host     string

	// Hooks
	Visitor web.Visitor
	URLSet  storage.URLSet
	Bus     event.Bus
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
		frontier.WithAutoAck(false),
		frontier.WithPrefetch(s.Prefetch),
		event.ConsumeWithContext(ctx),
	)
	if err != nil {
		return err
	}
	defer consumer.Close()

	deliveries, err := consumer.Consume(s.Host)
	if err != nil {
		return err
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	defer func() {
		log.WithField("host", s.Host).Info("stopping spider")
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case tm := <-s.sleep:
			tm = s.Wait - tm
			if tm < 0 {
				break
			}
			log.Debugf("sleeping for %v", tm)
			timer.Reset(tm)
			select {
			case <-timer.C:
			case <-ctx.Done():
				return nil
			}
		case msg := <-deliveries:
			log.WithFields(logrus.Fields{
				"key":      msg.RoutingKey,
				"tag":      msg.DeliveryTag,
				"exchange": msg.Exchange,
				"type":     msg.Type,
				"consumer": msg.ConsumerTag,
			}).Trace("message")
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
			go func() {
				err = s.handle(ctx, &req, publisher)
				if err == nil {
					if err = msg.Ack(false); err != nil {
						log.WithFields(logrus.Fields{
							"acknowledger":     fmt.Sprintf("%+v", msg.Acknowledger),
							"acknowledgerType": fmt.Sprintf("%T", msg.Acknowledger),
							"error":            err,
						}).Warning("could not acknowledge message")
					}
				}
				log.WithFields(logrus.Fields{
					"url":   req.URL,
					"depth": req.Depth,
				}).Debug("request handled")
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
		return err
	}
	s.Visitor.LinkFound(page.URL)

	s.mu.Lock()
	ok := s.robots.TestAgent(page.URL.Path, "*")
	s.mu.Unlock()
	if !ok {
		log.WithField("url", req.URL).Trace("found in robots.txt")
		return nil
	}
	if s.URLSet.Has(page.URL) {
		log.WithField("url", req.URL).Trace("already seen")
		return nil
	}

	// Assume that if handleRequest is called then it made a
	// request to the web server and so we need to wait for politness.
	fetchCtx, cancel := context.WithTimeout(ctx, time.Minute)
	err := page.FetchCtx(fetchCtx)
	if err != nil {
		log.WithError(err).Error("could not fetch page")
		cancel()
		return err
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
	s.Visitor.Visit(ctx, page)

	haslinks := len(page.Links) > 0
	// Only push the page's links if those pages will
	// be crawled in the future. If the page's links are
	// going to exceed the depth limit then they will be
	// discarded in the future so we should not push them
	// to the queue now.
	if haslinks && (s.Limit == 0 || page.Depth+1 < uint32(s.Limit)) {
		visited := s.URLSet.HasMulti(page.Links)
		done := ctx.Done()
		for i, l := range page.Links {
			select {
			case <-done:
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

			r := web.NewPageRequest(l, page.Depth+1)
			msg := amqp.Publishing{DeliveryMode: amqp.Persistent}
			if err = frontier.SetPageReqAsMessageBody(r, &msg); err != nil {
				log.WithError(err).Warn("could not convert page request to message body")
				continue
			}
			select {
			case <-done:
				return nil
			default:
			}
			err = pub.Publish(l.Host, msg)
			if err != nil {
				log.WithError(err).Error("could not publish message")
				continue
			}
		}
	}

	err = s.URLSet.Put(page.URL)
	if err != nil {
		log.WithError(err).WithField("url", page.URL).Warn("could not mark as seen")
	}
	if page.Redirected {
		err = s.URLSet.Put(page.RedirectedFrom)
		if err != nil {
			log.WithError(err).WithField("url", page.URL).Warn("could mark redirected url as seen")
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
