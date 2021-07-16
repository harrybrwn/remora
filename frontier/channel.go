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
	prefetch     int
}

func initChannel(
	ctx context.Context,
	ch *channel,
) error {
	ch.chOpen = sync.NewCond(&ch.mu)
	ch.reload = make(chan struct{})
	if ch.ctx == nil {
		ch.ctx, ch.cancel = context.WithCancel(ctx)
	}
	atomic.StoreInt32(&ch.open, 0)
	go ch.handleReload()
	return nil
}

func (c *channel) Close() error {
	log.Debug("channel: closing channel")
	defer close(c.reload)
	c.cancel()
	atomic.StoreInt32(&c.open, 0)
	return c.ch.Close()
}

func (c *channel) handleReload() {
	defer log.Debug("channel: ending channel reloads")
	c.reloadChannel() // initial channel
	for {
		select {
		case <-c.ctx.Done():
			log.Debug("channel: context canceled in reload handler")
			return
		case <-c.conndone:
			// c.cancel() // connection was closed on purpose
			if c.ch != nil {
				c.ch.Close()
			}
			log.Trace("channel: received connection done signal")
			return
		case <-c.notifyClosed:
			atomic.StoreInt32(&c.open, 0)
			// log.Debug("channel: close notify... reloading channel")
			// go func() { <-c.reload }() // make sure the reload channel doesn't block
			c.reloadChannel()
		case _, ok := <-c.reload:
			log.Debug("channel: received on reload channel")
			if !ok {
				// reload channel was closed
				return
			}
			atomic.StoreInt32(&c.open, 0)
			c.reloadChannel()
		}
	}
}

func (c *channel) reloadChannel() {
	log.Debug("channel: reloading channel")
	defer log.Debug("channel: channel reloaded")
	atomic.StoreInt32(&c.open, 0)

	c.mu.Lock()
	log.Debug("channel: reload lock acquired")
	defer c.mu.Unlock()
	atomic.StoreInt32(&c.open, 0)

	ch, err := c.conn.Channel()
	if err != nil {
		log.WithError(err).Error("channel: could not create new channel")
		return
	}
	log.Debug("channel: created new channel")

	var closeOld bool
	select {
	case <-c.notifyClosed:
		closeOld = false
	default:
		closeOld = true
	}

	if c.ch != nil && closeOld {
		// Close old channel in case its still open.
		log.Debug("channel: closing old channel")
		err := c.ch.Close()
		log.Debug("channel: old channel closed")
		if err != nil {
			log.WithError(err).Error("channel: could not close old channel")
		}
	}
	err = c.moveChannel(ch)
	if err != nil {
		log.WithError(err).Error("could not set inner channel")
		c.cancel() // this should error stop the channel
		return
	}
	log.Debug("channel: new channel set")
	atomic.StoreInt32(&c.open, 1)
	c.chOpen.Broadcast()
}

func (c *channel) wait() {
	c.mu.Lock()
	for c.notReady() {
		log.Debug("channel: waiting for reload")
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
	// TODO Upon connection reload, queued messages are unabled to be be acknowledged
	var deliveries = make(chan amqp.Delivery)
	go func() {
		defer close(deliveries)
		defer log.Trace("channel: consumer done with re-tries")
		for {
			var (
				ch  <-chan amqp.Delivery
				err error
			)
			select {
			case <-c.ctx.Done():
				log.Debug("channel: consumer context closed")
				return
			default:
			}
			log.Debug("channel: consumer is waiting for channel reload")
			c.wait()
			log.Debug("channel: consumer getting deliveries inner channel")
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
				log.WithError(err).Warn("channel: consumer could not consume")
				time.Sleep(ReloadDelay)
				goto NextConsumer
			}

			log.Debug("channel: consumer draining inner channel")
			// Pipe the messages along to the outer
			// caller's message channel.
			for {
				select {
				case reason := <-c.notifyCancel:
					log.WithField("reason", reason).Warn("channel: consumer canceled")
					goto NextConsumer
				case <-c.ctx.Done():
					log.Debug("channel: consumer context closed")
					return
				case msg, ok := <-ch:
					if !ok {
						log.Debug("channel: consumer inner channel closed")
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
		return nil
	case <-c.ctx.Done():
		return c.ctx.Err()
	}
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

func (c *channel) moveChannel(ch *amqp.Channel) error {
	err := ch.Qos(c.prefetch, 0, false)
	if err != nil {
		return err
	}
	c.ch = ch
	c.notifyCancel = ch.NotifyCancel(make(chan string))
	c.notifyClosed = ch.NotifyClose(make(chan *amqp.Error))
	return nil
}

func (c *channel) WithContext(ctx context.Context) {
	c.ctx, c.cancel = context.WithCancel(ctx)
}

func (c *channel) withPretch(prefetch int) {
	c.prefetch = prefetch
}
