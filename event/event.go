package event

import (
	"context"
	"errors"
	"io"

	"github.com/streadway/amqp"
)

type Bus interface {
	io.Closer
	Consumer(string, ...ConsumerOpt) (Consumer, error)
	Publisher(opts ...PublisherOpt) (Publisher, error)
}

type ConsumerOpt func(Consumer) error

type Consumer interface {
	io.Closer
	Consume(keys ...string) (<-chan amqp.Delivery, error)
	WithOpt(...ConsumerOpt) error
	// Canceled() <-chan string
}

type PublisherOpt func(Publisher) error

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

func NewChannelBus() *ChannelBus {
	bus := ChannelBus{
		pub: make(chan amqp.Publishing),
		del: make(chan amqp.Delivery),
	}
	bus.WithContext(context.Background())
	go func() {
		var count uint64
		for {
			select {
			case <-bus.ctx.Done():
				return
			case msg := <-bus.pub:
				bus.del <- pubToDelivery(msg, count)
				count++
			}
		}
	}()
	return &bus
}

type ChannelBus struct {
	pub    publisher
	del    consumer
	ctx    context.Context
	cancel context.CancelFunc
}

func (cb *ChannelBus) Consumer() (Consumer, error) {
	return cb.del, nil
}
func (cb *ChannelBus) Publisher() (Publisher, error) {
	return cb.pub, nil
}
func (cb *ChannelBus) WithContext(ctx context.Context) {
	cb.ctx, cb.cancel = context.WithCancel(ctx)
}
func (cb *ChannelBus) Close() error {
	cb.pub.Close()
	cb.del.Close()
	cb.cancel()
	return nil
}

type publisher chan amqp.Publishing

func (p publisher) Close() error { close(p); return nil }
func (p publisher) Publish(key string, msg amqp.Publishing) error {
	p <- msg
	return nil
}

type consumer chan amqp.Delivery

func (c consumer) Close() error                                    { close(c); return nil }
func (c consumer) Consume(...string) (<-chan amqp.Delivery, error) { return c, nil }
func (c consumer) WithOpt(...ConsumerOpt) error                    { return nil }

func setContext(i interface{}, ctx context.Context) error {
	setter, ok := i.(interface {
		WithContext(context.Context)
	})
	if !ok {
		return errors.New("cannot set context")
	}
	setter.WithContext(ctx)
	return nil
}

func pubToDelivery(msg amqp.Publishing, count uint64) amqp.Delivery {
	return amqp.Delivery{
		Acknowledger:    nil, // TODO figure this out
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
