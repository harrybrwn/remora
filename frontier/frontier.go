package frontier

import (
	"context"
	"errors"
	"io"
	"net/url"
	"sync"

	"github.com/harrybrwn/diktyo/web"
	"github.com/sirupsen/logrus"
	"github.com/streadway/amqp"
	"google.golang.org/protobuf/proto"
)

var log = logrus.New()

func SetLogger(l *logrus.Logger) { log = l }

type Queue interface {
	io.Closer
	Consumer(queue string, opts ...ConsumerOpt) (Consumer, error)
	Publisher(opts ...PublisherOpt) (Publisher, error)
}

type Consumer interface {
	io.Closer
	Prefetch(n int) error
	Consume(keys ...string) (<-chan amqp.Delivery, error)
	WithOpt(...ConsumerOpt) error

	// TODO add hooks for channel events, i.e. Channel.NotifyCancel, etc.
}

type Publisher interface {
	io.Closer
	Publish(key string, msg amqp.Publishing) error

	// TODO add some priority management hooks here
}

var DefaultExchangeKind = "topic"

type Frontier struct {
	Exchange Exchange
	conn     *amqp.Connection
}

type Exchange struct {
	Name       string
	Kind       string
	AutoDelete bool
	Durable    bool
}

func (e *Exchange) declare(ch *amqp.Channel) error {
	if e.Kind == "" {
		return nil
	}
	return ch.ExchangeDeclare(
		e.Name,
		e.Kind,
		e.Durable,
		e.AutoDelete,
		false, // internal
		false, // no wait
		nil,
	)
}

func (f *Frontier) Connect(url string) error {
	var err error
	f.conn, err = amqp.Dial(url)
	if err != nil {
		return err
	}
	return nil
}

func (f *Frontier) Consumer(queue string, opts ...ConsumerOpt) (Consumer, error) {
	ch, err := f.conn.Channel()
	if err != nil {
		return nil, err
	}
	err = f.Exchange.declare(ch)
	if err != nil {
		ch.Close()
		return nil, err
	}
	q, err := ch.QueueDeclare(queue, true, true, false, false, nil)
	if err != nil {
		ch.Close()
		return nil, err
	}
	c := &consumer{
		channel: channel{
			ch:       ch,
			exchange: f.Exchange.Name,
		},
		AutoAck: false,
		queue:   q.Name,
	}
	for _, o := range opts {
		err = o(c)
		if err != nil {
			ch.Close()
			return nil, err
		}
	}
	return c, nil
}

func (f *Frontier) Publisher(opts ...PublisherOpt) (Publisher, error) {
	ch, err := f.conn.Channel()
	if err != nil {
		return nil, err
	}
	err = f.Exchange.declare(ch)
	if err != nil {
		ch.Close()
		return nil, err
	}
	p := &publisher{
		channel: channel{
			exchange: f.Exchange.Name,
			ch:       ch,
		},
	}
	for _, o := range opts {
		err = o(p)
		if err != nil {
			ch.Close()
			return nil, err
		}
	}
	return p, nil
}

func (f *Frontier) Close() error {
	return f.conn.Close()
}

type channel struct {
	ch       *amqp.Channel
	exchange string
}

func (c *channel) Close() error { return c.ch.Close() }

type publisher struct {
	channel
	mu sync.Mutex // prevent multi-publishing
}

type PublisherOpt func(*publisher) error

func (p *publisher) Publish(key string, msg amqp.Publishing) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.ch.Publish(p.exchange, key, false, false, msg)
}

type consumer struct {
	channel

	AutoAck bool
	queue   string
	name    string
}

type ConsumerOpt func(*consumer) error

func WithAutoAck(auto bool) ConsumerOpt {
	return func(c *consumer) error { c.AutoAck = auto; return nil }
}

func WithPrefetch(n int) ConsumerOpt {
	return func(c *consumer) error { return c.ch.Qos(n, 0, false) }
}

func WithName(name string) ConsumerOpt {
	return func(c *consumer) error { c.name = name; return nil }
}

func WithKeys(keys ...string) ConsumerOpt {
	return func(c *consumer) error {
		if c.queue == "" {
			return errors.New("consumer queue not set")
		}
		return bindKeys(c, keys)
	}
}

func OnCancel(ch chan string) ConsumerOpt {
	return func(c *consumer) error { c.ch.NotifyCancel(ch); return nil }
}
func OnClose(ch chan *amqp.Error) ConsumerOpt {
	return func(c *consumer) error { c.ch.NotifyClose(ch); return nil }
}

func (c *consumer) WithOpt(opts ...ConsumerOpt) error {
	var err error
	for _, o := range opts {
		err = o(c)
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *consumer) Consume(keys ...string) (<-chan amqp.Delivery, error) {
	err := bindKeys(c, keys)
	if err != nil {
		return nil, err
	}
	return c.ch.Consume(c.queue, c.name, c.AutoAck, false, false, false, nil)
}

func bindKeys(c *consumer, keys []string) error {
	var err error
	for _, k := range keys {
		err = c.ch.QueueBind(c.queue, k, c.exchange, false, nil)
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *consumer) Prefetch(n int) error {
	return c.ch.Qos(n, 0, false)
}

func DeclareHostQueue(ch *amqp.Channel, host string) (amqp.Queue, error) {
	return ch.QueueDeclare(
		// fmt.Sprintf("spider.%s", host), // TODO add this
		host,
		true,  // durable
		false, // delete when unused
		false, // exclusive
		false, // no wait
		nil,   // arguments
	)
}

func PushRequest(ch *amqp.Channel, req *web.PageRequest) error {
	u, err := url.Parse(req.URL)
	if err != nil {
		return err
	}
	return PushRequestToHost(ch, req, u.Host)
}

func PushRequestToHost(ch *amqp.Channel, req *web.PageRequest, name string) error {
	raw, err := proto.Marshal(req)
	if err != nil {
		return err
	}
	return ch.Publish(
		"",
		name,
		false, false,
		amqp.Publishing{
			Body:         raw,
			DeliveryMode: amqp.Persistent,
			ContentType:  "application/vnd.google.protobuf",
			Priority:     0,
		},
	)
}

func NewRequestQueue(ch *amqp.Channel) *requestQueue {
	return &requestQueue{ch}
}

type requestQueue struct{ ch *amqp.Channel }

func (q *requestQueue) Consume(ctx context.Context, name string) (<-chan *web.PageRequest, error) {
	ch := make(chan *web.PageRequest)
	delivery, err := q.ch.Consume(name, "", false, false, false, false, nil)
	if err != nil {
		return nil, err
	}
	var (
		chanCancel = make(chan string)
	)
	q.ch.NotifyCancel(chanCancel)

	go func() {
		defer close(ch)
		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-delivery:
				err = msg.Ack(false)
				if err != nil {
					log.WithError(err).Error("could not acknowledge message")
					continue
				}
				req := new(web.PageRequest)
				err = proto.Unmarshal(msg.Body, req)
				if err != nil {
					log.WithError(err).Error("could not unmarshal request")
					continue
				}
				ch <- req
			case tag := <-chanCancel:
				log.WithField("tag", tag)
				return
			}
		}
	}()
	return ch, nil
}
