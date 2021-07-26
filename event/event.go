package event

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/streadway/amqp"
)

type Event struct {
	Priority uint8
	Body     []byte
	Tag      uint64
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

type ContextSetter interface {
	// TODO when generics are added, this should return itself
	WithContext(context.Context)
}

func NewChannelBus() *ChannelBus {
	bus := ChannelBus{queues: make(map[string]*queue)}
	bus.WithContext(context.Background())
	return &bus
}

type ChannelBus struct {
	mu     sync.Mutex
	queues map[string]*queue
	ctx    context.Context
	cancel context.CancelFunc
}

func (cb *ChannelBus) Consumer(key string, opts ...ConsumerOpt) (Consumer, error) {
	var err error
	cb.mu.Lock()
	defer cb.mu.Unlock()
	q, ok := cb.queues[key]
	if !ok {
		q, err = newqueue(opts)
		cb.queues[key] = q
	}
	return q, err
}

func (cb *ChannelBus) Publish(key string, msg amqp.Publishing) error {
	cb.mu.Lock()
	q, ok := cb.queues[key]
	cb.mu.Unlock()
	if !ok {
		return nil
	}
	select {
	case q.pub <- msg:
	case <-cb.ctx.Done():
	}
	return nil
}

func (cb *ChannelBus) PublishEvent(key string, msg Event) error {
	return cb.Publish(key, amqp.Publishing{
		Priority: msg.Priority,
		Body:     msg.Body,
	})
}

func (cb *ChannelBus) Publisher(opts ...PublisherOpt) (Publisher, error) {
	return cb, nil
}

func (cb *ChannelBus) WithContext(ctx context.Context) {
	cb.ctx, cb.cancel = context.WithCancel(ctx)
}

func (cb *ChannelBus) Close() error {
	cb.cancel()
	cb.mu.Lock()
	for _, q := range cb.queues {
		q.Close()
	}
	cb.mu.Unlock()
	return nil
}

type queue struct {
	pub    chan amqp.Publishing
	con    chan amqp.Delivery
	ctx    context.Context
	cancel context.CancelFunc
}

func newqueue(opts []ConsumerOpt) (*queue, error) {
	var q queue
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
	close(q.con)
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

func (q *queue) Consume(keys ...string) (<-chan amqp.Delivery, error) {
	go q.start(q.ctx, q.con)
	return q.con, nil
}

func (q *queue) start(ctx context.Context, c chan amqp.Delivery) {
	q.ctx, q.cancel = context.WithCancel(ctx)
	var count uint64 = 0
	for {
		select {
		case <-q.ctx.Done():
			return
		case c <- pubToDelivery(<-q.pub, count):
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
