package frontier

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/harrybrwn/remora/event"
	"github.com/sirupsen/logrus"
	"github.com/streadway/amqp"
)

const (
	queuename = "test"

	exchange  = "logs"
	consumers = 5
	level     = logrus.ErrorLevel
)

var testexchange = Exchange{
	Name:       "logs",
	Kind:       DefaultExchangeKind,
	Durable:    true,
	AutoDelete: false,
}

func Test(t *testing.T) {}

func TestConnectionRetry(t *testing.T) {
	t.Parallel()
	log.SetLevel(level)
	tmp := ReconnectDelay
	defer func() { ReconnectDelay = tmp }()
	ReconnectDelay = time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	front := Frontier{
		Exchange:   Exchange{Name: exchange, Durable: true, AutoDelete: true},
		RetryLimit: 2,
	}
	err := front.Connect(ctx, Connect{
		Host: "localhost", Port: 3002}) // make sure this won't connect
	if err == nil {
		t.Error("should have gotten an error on connect")
	}
	defer front.Close()
}

func TestConsumer(t *testing.T) {
	t.Parallel()
	log.SetLevel(level)
	signals := make(chan os.Signal, 5)
	signal.Notify(signals, os.Interrupt)
	timeout := time.Second * 1
	timeout = time.Millisecond * 250
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	var (
		front = &Frontier{Exchange: testexchange, RetryLimit: 5}
	)
	log.Info("connecting frontier")
	err := front.Connect(ctx, Connect{Host: "localhost", Port: 5672})
	if err != nil {
		t.Fatal(err)
	}
	defer front.Close()
	log.Info("consumer frontier connected")

	defer cancel()
	c, err := front.Consumer(
		queuename,
		WithAutoAck(true),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	for i := 0; i < consumers; i++ {
		c, err := front.Consumer(
			queuename,
			WithAutoAck(true),
			event.WithPrefetch(5),
		)
		if err != nil {
			t.Fatal(err)
		}
		go func(c event.Consumer, id int) {
			defer c.Close()
			deliveries, err := c.Consume(fmt.Sprintf("log.testing.%d", id))
			if err != nil {
				log.Println(err)
				return
			}
			for msg := range deliveries {
				fmt.Printf("[%q %q] %v %v => %s\n", msg.Exchange, msg.RoutingKey, msg.ConsumerTag, msg.DeliveryTag, msg.Body)
			}
		}(c, i)
	}

	select {
	case <-signals:
	case <-ctx.Done():
	}
}

func TestPublisher(t *testing.T) {
	t.Parallel()
	log.SetLevel(level)
	var (
		front = &Frontier{Exchange: testexchange, RetryLimit: 2}
		wait  time.Duration
	)
	wait = time.Millisecond
	timeout := time.Second * 2
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	err := front.Connect(
		ctx,
		Connect{Host: "localhost", Port: 5672},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer front.Close()
	pub, err := front.Publisher()
	if err != nil {
		t.Fatal(err)
	}
	defer pub.Close()

	var (
		done = make(chan struct{})
		tick = time.NewTicker(wait)
	)
	defer func() {
		tick.Stop()
		close(done)
	}()
	for i := 0; i < 10*consumers; i++ {
		select {
		case <-tick.C:
			key := fmt.Sprintf("log.%[1]d.%[1]d", i%consumers)
			err = pub.Publish(key, amqp.Publishing{
				DeliveryMode: amqp.Transient,
				Body:         []byte(fmt.Sprintf("hello from tick %d", i)),
			})
			if err != nil {
				t.Error(err)
			}
		case <-done:
			return
		}
	}
}

func TestChannel_wait(t *testing.T) {
	t.Parallel()
	log.SetLevel(level)
	var ch channel
	ch.chOpen = sync.NewCond(&ch.mu)
	ch.reload = make(chan struct{})
	ch.ctx, ch.cancel = context.WithCancel(context.Background())
	go func() {
		time.Sleep(time.Millisecond * 250)
		atomic.CompareAndSwapInt32(&ch.open, ch.open, 1)
		ch.chOpen.Signal()
	}()
	ch.wait()
	if ch.open != 1 {
		t.Error("expected the open flag to be 1")
	}
}

type channelbus struct {
	ctx        context.Context
	deliveries map[string]chan amqp.Delivery
	publishers map[string]chan amqp.Publishing

	copts []event.ConsumerOpt
	popts []event.PublisherOpt

	notifyCancel chan string
}

func newChannelBus() *channelbus {
	return &channelbus{
		ctx:          context.Background(),
		deliveries:   make(map[string]chan amqp.Delivery),
		publishers:   make(map[string]chan amqp.Publishing),
		notifyCancel: make(chan string),
	}
}

func (cb *channelbus) Close() error {
	for _, ch := range cb.deliveries {
		close(ch)
	}
	for _, ch := range cb.publishers {
		close(ch)
	}
	return nil
}

type acker struct{}

func (*acker) Ack(uint64, bool) error        { return nil }
func (*acker) Nack(uint64, bool, bool) error { return nil }
func (*acker) Reject(uint64, bool) error     { return nil }

func (cb *channelbus) Prefetch(n int) error { return nil }

func (cb *channelbus) Consume(keys ...string) (<-chan amqp.Delivery, error) {
	ch := make(chan amqp.Delivery)
	pubs := make(map[string]chan amqp.Publishing)
	for _, k := range keys {
		pub, ok := cb.publishers[k]
		if !ok {
			pubs[k] = pub
		}
	}
	go func() {
		var count uint32 = 0
		for {
			for key, pub := range pubs {
				select {
				case <-cb.ctx.Done():
					return
				case p := <-pub:
					ch <- amqp.Delivery{
						Acknowledger:    &acker{},
						Body:            p.Body,
						RoutingKey:      key,
						MessageCount:    count,
						DeliveryMode:    p.DeliveryMode,
						Timestamp:       p.Timestamp,
						ContentType:     p.ContentType,
						ContentEncoding: p.ContentEncoding,
					}
					count++
				}
			}
		}
	}()
	return ch, nil
}

func (cb *channelbus) WithOpt(opts ...event.ConsumerOpt) error {
	cb.copts = append(cb.copts, opts...)
	return nil
}

func (cb *channelbus) Consumer(queue string, opts ...event.ConsumerOpt) (event.Consumer, error) {
	cb.copts = opts
	return cb, nil
}

func (cb *channelbus) Publisher(opts ...event.PublisherOpt) (event.Publisher, error) {
	cb.popts = opts
	return cb, nil
}

func (cb *channelbus) Publish(key string, msg amqp.Publishing) error {
	pub, ok := cb.publishers[key]
	if !ok {
		return errors.New("cannot publish to " + key)
	}
	select {
	case pub <- msg:
	case <-cb.ctx.Done():
		return cb.ctx.Err()
	}
	return nil
}

func (cb *channelbus) Canceled() <-chan string {
	return cb.notifyCancel
}

func TestMockEventBus(t *testing.T) {
	bus := newChannelBus()
	defer bus.Close()
}
