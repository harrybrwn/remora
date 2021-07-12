package main

import (
	"context"
	"database/sql"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/go-redis/redis"
	"github.com/harrybrwn/diktyo/cmd"
	"github.com/harrybrwn/diktyo/db"
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
		hosts []string
		nop   bool
	)
	c := &cobra.Command{
		Use: "spider", Short: "Start a spider",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			if len(hosts) == 0 {
				return errors.New("no hosts to crawl with spider")
			}
			db, redis, err := getDataStores(conf)
			if err != nil {
				return errors.Wrap(err, "datastore connection failure")
			}
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
				db.Close()
				vis = &web.NoOpVisitor{}
			} else {
				vis = visitor.New(db)
			}
			err = front.Connect(frontier.Connect{
				Host: conf.MessageQueue.Host,
				Port: conf.MessageQueue.Port,
			})
			if err != nil {
				return errors.Wrap(err, "could not connect to frontier")
			}
			defer front.Close()

			for i, host := range hosts {
				spiders[i], err = newSpider(conf, host, vis, &front, storage.NewRedisURLSet(redis))
				if err != nil {
					return err
				}
				spiders[i].Wait = getWait(host, conf, cmd)
			}
			for _, sp := range spiders {
				log.WithFields(logrus.Fields{
					"wait": sp.Wait,
					"host": sp.host,
				}).Info("starting spider")
				go sp.start(ctx)
			}
			<-ctx.Done()
			return nil
		},
	}
	c.Flags().BoolVar(&nop, "nop", nop, "no operation when visiting a page")
	c.Flags().StringArrayVar(&hosts, "host", hosts, "host of website being crawled")
	c.Flags().IntVar(
		&conf.MessageQueue.Prefetch, "prefetch",
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
	host  string
	limit uint
	v     web.Visitor
	sleep chan time.Duration

	robots *robotstxt.RobotsData
	mu     sync.Mutex

	urlset   storage.URLSet
	front    *frontier.Frontier
	prefetch int
	Wait     time.Duration
}

func newSpider(
	conf *cmd.Config, host string, vis web.Visitor, front *frontier.Frontier, urlset storage.URLSet) (*spider, error) {
	var (
		err error
		sp  = spider{
			host:     host,
			limit:    conf.Depth,
			Wait:     conf.Sleep,
			v:        vis,
			front:    front,
			urlset:   urlset,
			sleep:    make(chan time.Duration),
			prefetch: conf.MessageQueue.Prefetch,
		}
	)
	sp.robots, err = web.GetRobotsTxT(host)
	if err != nil {
		return nil, err
	}
	if sp.Wait == 0 {
		sp.Wait = 1
	}
	return &sp, nil
}

func (s *spider) start(ctx context.Context) error {
	publisher, err := s.front.Publisher()
	if err != nil {
		log.WithError(err).Error("could not create publisher")
		return err
	}
	consumer, err := s.front.Consumer(
		s.host,
		frontier.WithAutoAck(false),
		frontier.WithPrefetch(s.prefetch),
	)
	if err != nil {
		return err
	}
	defer consumer.Close()

	deliveries, err := consumer.Consume(s.host)
	if err != nil {
		return err
	}
	timer := time.NewTimer(0)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case tm := <-s.sleep:
			tm = s.Wait - tm
			if tm < 0 {
				break
			}
			log.Infof("sleeping for %v", tm)
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
			log.WithFields(logrus.Fields{
				"url":   req.URL,
				"depth": req.Depth,
			}).Trace("handle request")

			go func() {
				err = s.handle(ctx, &req, publisher)
				if err == nil {
					if err = msg.Ack(false); err != nil {
						log.WithError(err).Warning("could not acknowledge message")
					}
				}
			}()
		}
	}
}

func (s *spider) handle(ctx context.Context, req *web.PageRequest, pub frontier.Publisher) error {
	var (
		start = time.Now()
		page  = web.NewPageFromString(req.URL, req.Depth)
	)
	if page == nil {
		log.WithField("url", req.URL).Error("could not create page from page request")
		return errors.New("could not parse page request url")
	}
	s.v.LinkFound(page.URL)

	s.mu.Lock()
	ok := s.robots.TestAgent(page.URL.Path, "*")
	s.mu.Unlock()
	if !ok {
		log.WithField("url", req.URL).Trace("found in robots.txt")
		return nil
	}
	if s.urlset.Has(page.URL) {
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
		if ok || s.urlset.Has(page.URL) {
			return nil
		}
	}
	s.v.Visit(ctx, page)

	haslinks := len(page.Links) > 0
	// Only push the page's links if those pages will
	// be crawled in the future. If the page's links are
	// going to exceed the depth limit then they will be
	// discarded in the future so we should not push them
	// to the queue now.
	if haslinks && (s.limit == 0 || page.Depth+1 < uint32(s.limit)) {
		visited := s.urlset.HasMulti(page.Links)
		done := ctx.Done()
		for i, l := range page.Links {
			select {
			case <-done:
			default:
			}
			if l.Host == s.host {
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

	err = s.urlset.Put(page.URL)
	if err != nil {
		log.WithError(err).WithField("url", page.URL).Warn("could not mark as seen")
	}
	if page.Redirected {
		err = s.urlset.Put(page.RedirectedFrom)
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
