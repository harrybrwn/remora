package event

import (
	"context"
	"fmt"
	"io"
	"sync"

	"github.com/pkg/errors"
	"github.com/streadway/amqp"
)

var (
	ErrNoQueue            = errors.New("queue does not exist")
	ErrWrongConsumerType  = errors.New("wrong consumer type")
	ErrWrongPublisherType = errors.New("wrong publisher type")
)

type Event struct {
	Tag      uint64
	Priority uint8
	Body     []byte
}

type ConsumerOpt func(Consumer) error

type PublisherOpt func(Publisher) error

type Bus interface {
	io.Closer
	Consumer(string, ...ConsumerOpt) (Consumer, error)
	Publisher(opts ...PublisherOpt) (Publisher, error)
}

type Consumer interface {
	io.Closer
	Consume(keys ...string) (<-chan amqp.Delivery, error)
}

type Publisher interface {
	io.Closer
	Publish(key string, msg amqp.Publishing) error
}

func ConsumeWithContext(ctx context.Context) ConsumerOpt {
	return func(c Consumer) error { return setContext(c, ctx) }
}

func PublishWithContext(ctx context.Context) PublisherOpt {
	return func(p Publisher) error { return setContext(p, ctx) }
}

func WithKeys(keys ...string) ConsumerOpt {
	return func(c Consumer) error {
		switch v := c.(type) {
		case interface{ BindKeys(...string) error }:
			err := v.BindKeys(keys...)
			if err != nil {
				return errors.Wrap(err, "consumer failed to bind routing keys to queue")
			}
			return nil
		default:
			return errors.New("could not bind keys")
		}
	}
}

type qos interface {
	Qos(prefetchCount int, prefetchSize int, global bool) error
}

type prefetchable interface {
	WithPrefetch(int)
}

func WithPrefetch(n int) ConsumerOpt {
	return func(c Consumer) error {
		switch v := c.(type) {
		case qos:
			return v.Qos(n, 0, false)
		case prefetchable:
			v.WithPrefetch(n)
			return nil
		case interface{ WithPrefetch(int) error }:
			return v.WithPrefetch(n)
		case interface{ Qos(int) error }:
			return v.Qos(n)
		default:
			return errors.Wrap(ErrWrongConsumerType, "could not set prefetch")
		}
	}
}

type ContextSetter interface {
	// TODO when generics are added, this should return itself
	WithContext(context.Context)
}

func NewChannelBus() *ChannelBus {
	return NewChannelBusContext(context.Background())
}

func NewChannelBusContext(ctx context.Context) *ChannelBus {
	bus := ChannelBus{queues: make(map[string]*queue)}
	bus.WithContext(ctx)
	return &bus
}

type ChannelBus struct {
	mu       sync.Mutex
	queues   map[string]*queue
	ctx      context.Context
	cancel   context.CancelFunc
	pubhooks []func(*amqp.Publishing)
	conhooks []func(*amqp.Delivery)
}

func (cb *ChannelBus) Consumer(key string, opts ...ConsumerOpt) (Consumer, error) {
	var err error
	cb.mu.Lock()
	defer cb.mu.Unlock()
	q, ok := cb.queues[key]
	if !ok {
		q, err = newqueue(cb.ctx, opts)
		cb.queues[key] = q
		for _, k := range q.keys {
			cb.queues[k] = q
		}
	}
	return q, err
}

func (cb *ChannelBus) Publish(key string, msg amqp.Publishing) error {
	cb.mu.Lock()
	q, ok := cb.queues[key]
	cb.mu.Unlock()
	if !ok {
		return errors.Wrap(ErrNoQueue, fmt.Sprintf("queue %q not found", key))
	}
	// Non-blocking publish so we can imitate a real message queue.
	go func() {
		for _, h := range cb.pubhooks {
			h(&msg)
		}
		select {
		case <-cb.ctx.Done():
			return
		case q.pub <- msg:
			return
		}
	}()
	return nil
}

func (cb *ChannelBus) PublishEvent(key string, msg Event) error {
	return cb.Publish(key, amqp.Publishing{
		Priority: msg.Priority,
		Body:     msg.Body,
	})
}

func (cb *ChannelBus) Publisher(opts ...PublisherOpt) (Publisher, error) {
	var err, e error
	for _, o := range opts {
		if e = o(cb); e != nil && err == nil {
			err = e
		}
	}
	return cb, err
}

func (cb *ChannelBus) WithContext(ctx context.Context) {
	cb.ctx, cb.cancel = context.WithCancel(ctx)
}

func (cb *ChannelBus) Close() error {
	cb.cancel()
	return nil
}

type queue struct {
	pub    chan amqp.Publishing
	con    chan amqp.Delivery
	ctx    context.Context
	cancel context.CancelFunc
	keys   []string

	consumeHooks []func(*amqp.Delivery)
}

func newqueue(ctx context.Context, opts []ConsumerOpt) (*queue, error) {
	var q queue
	q.WithContext(ctx)
	err := q.WithOpt(opts...)
	if q.pub == nil {
		q.pub = make(chan amqp.Publishing)
	}
	if q.con == nil {
		q.con = make(chan amqp.Delivery)
	}
	return &q, err
}

func (q *queue) Close() error {
	q.cancel()
	close(q.pub)
	return nil
}

func (q *queue) WithContext(ctx context.Context) {
	q.ctx, q.cancel = context.WithCancel(ctx)
}

func (q *queue) WithPrefetch(n int) {
	q.pub = make(chan amqp.Publishing, n)
	q.con = make(chan amqp.Delivery, n)
}

func (q *queue) Qos(n int, _ int, _ bool) error {
	q.WithPrefetch(n)
	return nil
}

func (q *queue) BindKeys(keys ...string) error {
	q.keys = append(q.keys, keys...)
	return nil
}

func (q *queue) Consume(...string) (<-chan amqp.Delivery, error) {
	go start(q.ctx, q.con, q.pub, q.consumeHooks)
	return q.con, nil
}

func start(
	ctx context.Context,
	con chan<- amqp.Delivery,
	pub <-chan amqp.Publishing,
	hooks []func(*amqp.Delivery),
) {
	var count uint64 = 0
	defer close(con)
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-pub:
			if !ok {
				return
			}
			delivery := pubToDelivery(msg, count)
			for _, h := range hooks {
				h(&delivery)
			}
			con <- delivery
			count++
		}
	}
}

func (q *queue) WithOpt(opts ...ConsumerOpt) error {
	var e, err error
	for _, o := range opts {
		e = o(q)
		if e != nil && err == nil {
			err = e
		}
	}
	return err
}

func setContext(i interface{}, ctx context.Context) error {
	setter, ok := i.(interface{ WithContext(context.Context) })
	if !ok {
		return errors.New("cannot set context")
	}
	setter.WithContext(ctx)
	return nil
}

func pubToDelivery(msg amqp.Publishing, count uint64) amqp.Delivery {
	return amqp.Delivery{
		Acknowledger:    &acknowledger{count},
		ContentType:     msg.ContentType,
		ContentEncoding: msg.ContentEncoding,
		DeliveryMode:    msg.DeliveryMode,
		Priority:        msg.Priority,
		CorrelationId:   msg.CorrelationId,
		ReplyTo:         msg.ReplyTo,
		Expiration:      msg.Expiration,
		MessageId:       msg.MessageId,
		Timestamp:       msg.Timestamp,
		Type:            msg.Type,
		UserId:          msg.UserId,
		AppId:           msg.AppId,
		ConsumerTag:     "",
		MessageCount:    uint32(count),
		DeliveryTag:     count,
		Redelivered:     false,
		Exchange:        "",
		RoutingKey:      "",
		Body:            msg.Body,
	}
}

type acknowledger struct{ n uint64 }

func (*acknowledger) Nack(uint64, bool, bool) error { return nil }
func (*acknowledger) Reject(uint64, bool) error     { return nil }
func (a *acknowledger) Ack(n uint64, _ bool) error {
	if n != a.n {
		return errors.New("wrong delivery tag")
	}
	return nil
}
