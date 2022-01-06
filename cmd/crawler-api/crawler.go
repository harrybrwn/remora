package main

/*
import (
	"context"
	"encoding/hex"
	"fmt"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/harrybrwn/remora/internal/que"
	"github.com/harrybrwn/remora/web"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	"google.golang.org/protobuf/proto"
)

type crawler struct {
	host       string // crawling hostname
	key        string // rabbitmq routing key
	consumer   que.AMQPChannel
	deliveries <-chan que.Delivery

	running uint32
	ctx     context.Context
	cancel  context.CancelFunc

	fetcher web.Fetcher
	robots  web.RobotsController
}

func (c *crawler) init(queue *queue, host string) error {
	var err error
	c.consumer, err = queue.conn.Channel()
	if err != nil {
		return err
	}
	err = queue.DeclareExchange(c.consumer)
	if err != nil {
		return err
	}
	q, err := c.consumer.QueueDeclare(
		host, true, false, false, false,
		que.AMQPTable{"x-queue-mode": "lazy"},
	)
	if err != nil {
		return err
	}
	err = c.consumer.QueueBind(q.Name, c.key, queue.exchange, false, nil)
	if err != nil {
		return err
	}
	c.deliveries, err = c.consumer.Consume(
		q.Name, "",
		true, false,
		false, false, nil,
	)
	if err != nil {
		return err
	}
	c.robots, err = web.NewRobotCtrl(host, []string{"*", web.UserAgent})
	if err != nil {
		return err
	}
	return nil
}

func (c *crawler) Stop() error {
	log.WithFields(logrus.Fields{
		"host": c.host,
	}).Info("stopping crawler")
	c.cancel()
	atomic.StoreUint32(&c.running, 0)
	return c.consumer.Close()
}

func (c *crawler) Running() bool {
	return atomic.LoadUint32(&c.running) == 1
}

func (c *crawler) Start(ctx context.Context) error {
	atomic.StoreUint32(&c.running, 1)
	c.ctx, c.cancel = context.WithCancel(ctx)
	ctx = c.ctx

	// TODO move all below to c.loop then replace with `go c.loop()`
	go c.loop(ctx)

	ticker := time.NewTicker(time.Second) // TODO get this from configuration
	defer func() {
		log.WithFields(logrus.Fields{
			"host": c.host,
		}).Info("exiting crawl loop")
		ticker.Stop()
		atomic.StoreUint32(&c.running, 0)
	}()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if !c.recv(ctx) {
				return errors.New("crawler stopped")
			}
		}
	}
}

func (c *crawler) loop(ctx context.Context) {
}

func (c *crawler) recv(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return false
	case msg, ok := <-c.deliveries:
		if !ok {
			log.WithField("host", c.host).Warn("deliveries channel closed")
			return false // TODO handle this
		}
		go func() {
			var req web.PageRequest
			err := proto.Unmarshal(msg.Body, &req)
			if err != nil {
				log.WithError(err).Error("could not deserialized page request")
				return
			}
			msg.Ack(false)
			log.WithFields(logrus.Fields{
				"url":         req.URL,
				"crawl_depth": req.Depth,
				"key":         string(req.Key),
				"hex_key":     hex.EncodeToString(req.Key),
			}).Debug("received page request")
			c.handle(ctx, &req)
		}()
	}
	return true
}

func (c *crawler) handle(ctx context.Context, req *web.PageRequest) {
	u, err := url.Parse(req.URL)
	if err != nil {
		log.WithFields(logrus.Fields{"error": err, "url": req.URL}).Warn("could not parse url")
		return
	}
	if c.robots.ShouldSkip(u) {
		return
	}
	pageCtx, cancel := context.WithTimeout(ctx, time.Minute)
	page, err := c.fetcher.Fetch(pageCtx, req)
	if err != nil {
		cancel()
		return
	}
	cancel()
	if page.URL == nil {
		fmt.Println("nil url")
	}
}

*/
