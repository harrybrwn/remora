package frontier

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/streadway/amqp"
)

var (
	ReloadDelay = time.Second * 2
)

type channelCreator interface {
	Channel() (*amqp.Channel, error)
}

type reloadable interface {
	Reload() error
}

type channel struct {
	ch       *amqp.Channel
	exchange string

	conn     channelCreator
	conndone chan struct{} // notified when connection is closed
	reload   chan struct{} // should reload channel events

	// lock and notify changes in channel pointer
	mu     sync.Mutex
	chOpen *sync.Cond
	open   int32
	ctx    context.Context
	cancel context.CancelFunc

	notifyCancel chan string
	notifyClosed chan *amqp.Error
}

func initChannel(
	ctx context.Context,
	ch *channel,
) error {
	ch.chOpen = sync.NewCond(&ch.mu)
	// ch.reload = reload
	ch.reload = make(chan struct{})
	ch.ctx, ch.cancel = context.WithCancel(ctx)
	atomic.StoreInt32(&ch.open, 0)
	go ch.handleReload()
	return nil
}

func (c *channel) Close() error {
	defer close(c.reload)
	c.cancel()
	atomic.StoreInt32(&c.open, 0)
	return c.ch.Close()
}

func (c *channel) handleReload() {
	c.reloadChannel() // initial channel
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-c.conndone:
			c.cancel() // connection was closed on purpose
			if c.ch != nil {
				c.ch.Close()
			}
			return
		case _, ok := <-c.reload:
			if !ok {
				// channel was closed
				return
			}
			atomic.StoreInt32(&c.open, 0)
			log.Debug("reloading channel")
		}
		c.reloadChannel()
	}
}

func (c *channel) reloadChannel() {
	c.mu.Lock()
	if c.ch != nil {
		// Close old channel in case its still open.
		c.ch.Close()
	}
	ch, err := c.conn.Channel()
	if err != nil {
		c.mu.Unlock()
		return
	}
	c.moveChannel(ch)
	atomic.StoreInt32(&c.open, 1)
	c.chOpen.Broadcast()
	c.mu.Unlock()
}

func (c *channel) wait() {
	c.mu.Lock()
	for c.notReady() {
		c.chOpen.Wait()
	}
	c.mu.Unlock()
}

func (c *channel) Publish(
	exchange, key string,
	mandatory, immediate bool,
	msg amqp.Publishing,
) error {
	if err := c.ctx.Err(); err != nil {
		return amqp.ErrClosed
	}

	// wait for channel pointer, does not
	// protect against multi-threaded publishing
	// which is not safe.
	c.wait()

	return c.ch.Publish(
		exchange,
		key,
		mandatory,
		immediate,
		msg,
	)
}

func (c *channel) Consume(
	queue, consumer string,
	autoAck, exclusive, noLocal, noWait bool,
	args amqp.Table,
) (<-chan amqp.Delivery, error) {
	var deliveries = make(chan amqp.Delivery)
	go func() {
		for {
			var (
				ch  <-chan amqp.Delivery
				err error
			)
			select {
			case <-c.ctx.Done():
				close(deliveries)
				return
			case <-c.notifyClosed:
				log.Debug("channel close notify: reloading channel")
				c.reloadChannel()
			default:
			}
			c.wait()

			ch, err = c.ch.Consume(
				queue,
				consumer,
				autoAck,
				exclusive,
				noLocal,
				noWait,
				args,
			)
			if err != nil {
				// TODO: handle this error
				log.WithError(err).Warn("could not consume")
				goto NextConsumer
			}
			// Pipe the messages along to the outer
			// caller's message channel.
			for {
				select {
				case <-c.ctx.Done():
					close(deliveries)
					return
				case msg, ok := <-ch:
					if !ok {
						log.Debug("inner consumer closed")
						goto NextConsumer
					}
					deliveries <- msg
				}
			}
		NextConsumer:
		}
	}()
	return deliveries, nil
}

func (c *channel) Reload() error {
	select {
	case c.reload <- struct{}{}:
	case <-c.ctx.Done():
		return c.ctx.Err()
	}
	return nil
}

// For interfaces and whatnot
func (c *channel) Channel() (*amqp.Channel, error) {
	c.wait()
	return c.ch, nil
}

func (ch *channel) Canceled() <-chan string {
	return ch.notifyCancel
}

func (c *channel) notReady() bool {
	return atomic.LoadInt32(&c.open) == 0
}

func (c *channel) moveChannel(ch *amqp.Channel) {
	c.ch = ch
	c.notifyCancel = make(chan string)
	c.notifyClosed = make(chan *amqp.Error)
	c.ch.NotifyCancel(c.notifyCancel)
	c.ch.NotifyClose(c.notifyClosed)
}
