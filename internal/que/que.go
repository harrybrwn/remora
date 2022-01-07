package que

import (
	"github.com/harrybrwn/remora/event"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

var log = logrus.StandardLogger()

type Message struct {
	Tag      uint64
	Priority uint8
	Route    string
	Body     []byte
}

func NewMessageQueue(conn AMQPConnection, exchange string) (*MessageQueue, error) {
	return &MessageQueue{
		conn: conn,
		Exchange: &Exchange{
			Name: exchange,
			Kind: ExchangeTopic,
		},
	}, nil
}

type MessageQueue struct {
	conn     AMQPConnection
	Exchange *Exchange
}

func (mq *MessageQueue) Connect(url string) (err error) {
	mq.conn, err = AMQPDial(url)
	return err
}

type Exchange struct {
	Name       string
	Kind       string
	AutoDelete bool
	Durable    bool
	Internal   bool
	NoWait     bool
	Args       AMQPTable
}

func (e *Exchange) declare(ch AMQPChannel) error {
	return ch.ExchangeDeclare(
		e.Name,
		e.Kind,
		e.Durable,
		e.AutoDelete,
		e.Internal,
		e.NoWait,
		e.Args,
	)
}

func (mq *MessageQueue) Close() error { return mq.conn.Close() }

func (mq *MessageQueue) channel() (*mqChannel, error) {
	ch, err := mq.conn.Channel()
	if err != nil {
		return nil, err
	}
	var exchange string
	if mq.Exchange != nil {
		exchange = mq.Exchange.Name
		err = mq.Exchange.declare(ch)
		if err != nil {
			return nil, err
		}
	}
	return &mqChannel{ch: ch, exchange: exchange}, nil
}

type QueueDeclareOptions struct {
	Name       string
	Durable    bool
	AutoDelete bool
	Exclusive  bool
	NoWait     bool
	Args       AMQPTable
}

func (mq *MessageQueue) Publisher(opts ...event.PublisherOpt) (event.Publisher, error) {
	var (
		err error
		p   mqPublisher
	)
	p.mqChannel, err = mq.channel()
	if err != nil {
		return nil, err
	}

	for _, opt := range opts {
		if err = opt(&p); err != nil {
			return nil, err
		}
	}
	return &p, nil
}

func (mq *MessageQueue) Consumer(
	queue string,
	opts ...event.ConsumerOpt,
) (event.Consumer, error) {
	var (
		err error
		c   mqConsumer
	)

	c.mqChannel, err = mq.channel()
	if err != nil {
		return nil, err
	}
	for _, opt := range opts {
		if err = opt(&c); err != nil {
			c.mqChannel.ch.Close()
			return nil, err
		}
	}
	c.queue = queue
	c.opts.Queue = queue
	c.queueOpts.Name = queue
	if err = c.declareQueue(); err != nil {
		return nil, errors.Wrap(err, "could not declare queue")
	}
	for _, k := range c.keys {
		err = c.ch.QueueBind(c.queue, k, c.exchange, false, nil)
		if err != nil {
			return nil, err
		}
	}
	c.keys = nil // clear the routing keys
	return &c, nil
}

type mqChannel struct {
	ch        AMQPChannel
	exchange  string
	queueOpts QueueDeclareOptions
}

func (ch mqChannel) declareQueue() error {
	if len(ch.queueOpts.Name) == 0 {
		return nil
	}
	_, err := ch.ch.QueueDeclare(
		ch.queueOpts.Name,
		ch.queueOpts.Durable,
		ch.queueOpts.AutoDelete,
		ch.queueOpts.Exclusive,
		ch.queueOpts.NoWait,
		ch.queueOpts.Args,
	)
	return err
}

func (ch *mqChannel) Close() error {
	return ch.ch.Close()
}

type mqConsumer struct {
	*mqChannel
	opts  ConsumeOptions
	queue string
	keys  []string
}

func (c *mqConsumer) Consume(keys ...string) (<-chan Delivery, error) {
	if err := c.BindKeys(keys...); err != nil {
		return nil, err
	}
	deliveries, err := c.ch.Consume(
		c.queue,
		c.opts.Consumer,
		c.opts.AutoAck,
		c.opts.Exclusive,
		c.opts.NoLocal,
		c.opts.NoWait,
		c.opts.Args,
	)
	if err != nil {
		return nil, err
	}
	return deliveries, nil
}

// BindKeys makes event.WithKeys work
func (c *mqConsumer) BindKeys(keys ...string) error {
	c.keys = append(c.keys, keys...)
	return nil
}

type mqPublisher struct {
	*mqChannel
	opts PublishOptions
}

func (ch *mqPublisher) Publish(key string, msg Publishing) error {
	ch.mqChannel.ch.Publish(
		ch.exchange,
		key,
		ch.opts.Mandatory,
		ch.opts.Immediate,
		msg,
	)
	return nil
}

func (ch *mqChannel) Qos(count, size int, global bool) error {
	return ch.ch.Qos(count, size, global)
}

func (ch *mqChannel) WithPrefetch(n int) error {
	return ch.Qos(n, 0, false)
}

// see github.com/streadway/amqp.(*Channel).Consume function arguments
type ConsumeOptions struct {
	Queue     string
	Consumer  string
	AutoAck   bool
	Exclusive bool
	NoLocal   bool
	NoWait    bool
	Args      AMQPTable
}

// see github.com/streadway/amqp.(*Channel).Publish function arguments
type PublishOptions struct {
	Exchange  string
	Key       string
	Mandatory bool
	Immediate bool
	Message   AMQPTable
}

func WithConsumeOptions(o *ConsumeOptions) event.ConsumerOpt {
	return func(c event.Consumer) error {
		ch, ok := c.(*mqConsumer)
		if !ok {
			return event.ErrWrongConsumerType
		}
		if len(ch.opts.Queue) == 0 {
			ch.opts.Queue = o.Queue
		}
		if ch.opts.Args == nil {
			ch.opts.Args = o.Args
		}
		if len(ch.opts.Consumer) == 0 {
			ch.opts.Consumer = o.Consumer
		}
		ch.opts.AutoAck = o.AutoAck
		ch.opts.Exclusive = o.Exclusive
		ch.opts.NoLocal = o.NoLocal
		ch.opts.NoWait = o.NoWait
		return nil
	}
}

func WithConsumerQueueOpts(opts *QueueDeclareOptions) event.ConsumerOpt {
	return consumerOptFn(func(mc *mqConsumer) error {
		if opts.Args != nil {
			mc.queueOpts.Args = opts.Args
		}
		mc.queueOpts.AutoDelete = opts.AutoDelete
		mc.queueOpts.Durable = opts.Durable
		mc.queueOpts.Exclusive = opts.Exclusive
		mc.queueOpts.NoWait = opts.NoWait
		return nil
	})
}

func WithPublishQueue(o *QueueDeclareOptions) event.PublisherOpt {
	return publisherOptFn(func(mp *mqPublisher) error {
		_, err := mp.ch.QueueDeclare(
			o.Name,
			o.Durable,
			o.AutoDelete,
			o.Exclusive,
			o.NoWait,
			o.Args,
		)
		return err
	})
}

func WithPublishOpts(o *PublishOptions) event.PublisherOpt {
	return publisherOptFn(func(mp *mqPublisher) error {
		if len(o.Exchange) != 0 {
			mp.opts.Exchange = o.Exchange
		}
		if len(o.Key) != 0 {
			mp.opts.Key = o.Key
		}
		mp.opts.Immediate = o.Immediate
		mp.opts.Mandatory = o.Mandatory
		mp.opts.Message = o.Message
		return nil
	})
}

func consumerOptFn(fn func(*mqConsumer) error) event.ConsumerOpt {
	return func(c event.Consumer) error {
		co, ok := c.(*mqConsumer)
		if !ok {
			return event.ErrWrongConsumerType
		}
		return fn(co)
	}
}

func publisherOptFn(fn func(*mqPublisher) error) event.PublisherOpt {
	return func(p event.Publisher) error {
		pb, ok := p.(*mqPublisher)
		if !ok {
			return event.ErrWrongPublisherType
		}
		return fn(pb)
	}
}
