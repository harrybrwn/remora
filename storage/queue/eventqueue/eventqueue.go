package eventqueue

import (
	"context"
	"sync"

	"github.com/dgraph-io/badger/v3"
	"github.com/harrybrwn/remora/event"
	"github.com/harrybrwn/remora/storage/queue"
	"github.com/streadway/amqp"
)

type EventQueue struct {
	db     *badger.DB
	mu     sync.Mutex
	queues map[string]queue.Queue
}

type q struct {
	key    string
	bus    *EventQueue
	q      queue.Queue
	ctx    context.Context
	cancel context.CancelFunc
}

func New(db *badger.DB) *EventQueue {
	return &EventQueue{
		db:     db,
		queues: make(map[string]queue.Queue),
	}
}

func (eq *EventQueue) Close() error {
	var e, err error
	eq.mu.Lock()
	for _, q := range eq.queues {
		e = q.Close()
		if e != nil && err == nil {
			err = e
		}
	}
	eq.mu.Unlock()
	return err
}

func (eq *EventQueue) Consumer(key string, opts ...event.ConsumerOpt) (event.Consumer, error) {
	var e, err error
	eq.mu.Lock()
	k, ok := eq.queues[key]
	if !ok {
		k = queue.New(eq.db, []byte(key))
		eq.queues[key] = k
	}
	eq.mu.Unlock()
	consumer := &q{q: k, bus: eq, key: key}
	consumer.WithContext(context.Background())
	for _, o := range opts {
		if e = o(consumer); e != nil && err == nil {
			err = e
		}
	}
	return consumer, err
}

type publisher struct {
	bus     *EventQueue
	ctx     context.Context
	pushall bool
}

func (eq *EventQueue) Publisher(opts ...event.PublisherOpt) (event.Publisher, error) {
	var e, err error
	p := &publisher{bus: eq, ctx: context.Background()}
	for _, o := range opts {
		if e = o(p); e != nil && err == nil {
			err = e
		}
	}
	return p, err
}

func (q *q) Close() error {
	q.cancel()
	return q.q.Close()
}

func (q *q) WithContext(ctx context.Context) {
	q.ctx, q.cancel = context.WithCancel(ctx)
}

func (q *q) Consume(keys ...string) (<-chan amqp.Delivery, error) {
	delivery := make(chan amqp.Delivery)
	var count uint64 = 0
	go func() {
		defer close(delivery)
		for {
			res, err := q.q.Pop()
			if err != nil {
				break
			}
			var msg = amqp.Delivery{
				Body:         res,
				Acknowledger: q,
				DeliveryTag:  count,
				MessageCount: uint32(count),
			}
			select {
			case <-q.ctx.Done():
				return
			case delivery <- msg:
			}
			count++
		}
	}()
	return delivery, nil
}

func (q *q) BindKeys(keys ...string) error {
	q.bus.mu.Lock()
	for _, k := range keys {
		if _, ok := q.bus.queues[k]; !ok {
			q.bus.queues[k] = q.q
		}
	}
	q.bus.mu.Unlock()
	return nil
}

func (q *q) Ack(uint64, bool) error        { return nil }
func (q *q) Nack(uint64, bool, bool) error { return nil }
func (q *q) Reject(uint64, bool) error     { return nil }

func (q *q) WithOpt(opts ...event.ConsumerOpt) (err error) {
	for _, o := range opts {
		err = o(q)
		if err != nil {
			return err
		}
	}
	return nil
}

func PushAll(pub event.Publisher) error {
	p, ok := pub.(*publisher)
	if ok {
		p.pushall = true
	}
	return nil
}

func (p *publisher) Close() error                    { return nil }
func (p *publisher) WithContext(ctx context.Context) { p.ctx = ctx }
func (p *publisher) Publish(key string, msg amqp.Publishing) error {
	p.bus.mu.Lock()
	k, ok := p.bus.queues[key]
	p.bus.mu.Unlock()
	if !ok {
		if p.pushall {
			k = queue.New(p.bus.db, []byte(key))
			p.bus.mu.Lock()
			p.bus.queues[key] = k
			p.bus.mu.Unlock()
		} else {
			return nil
		}
	}
	return k.Put(msg.Body)
}

var (
	_ event.Bus         = (*EventQueue)(nil)
	_ amqp.Acknowledger = (*q)(nil)
)
