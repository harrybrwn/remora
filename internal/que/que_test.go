package que

import (
	"context"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/matryer/is"
	"github.com/sirupsen/logrus"
	"github.com/streadway/amqp"
)

var (
	spec = amqp.URI{
		Scheme:   "amqp",
		Host:     "localhost",
		Port:     5672,
		Username: "guest",
		Password: "guest",
	}
)

func init() {
	log.SetOutput(os.Stdout)
	log.SetLevel(logrus.TraceLevel)
}

func TestMessageQueue(t *testing.T) {
	// t.Skip()
	is := is.New(t)
	mq := MessageQueue{Exchange: &Exchange{
		Name: "test_exchange",
		Kind: ExchangeTopic,
		// Kind:       ExchangeDirect,
		Durable:    true,
		AutoDelete: true,
	}}
	is.NoErr(mq.Connect(spec.String()))
	defer mq.Close()

	con, err := mq.Consumer(
		"q",
		// event.WithKeys("q.*"),
		WithConsumerQueueOpts(&QueueDeclareOptions{
			Durable:    false,
			AutoDelete: true,
		}),
	)
	is.NoErr(err)
	pub, err := mq.Publisher()
	is.NoErr(err)

	go func() {
		time.Sleep(time.Millisecond * 250)
		is.NoErr(pub.Publish("q", Publishing{Body: []byte("help")}))
		// is.NoErr(pub.Publish("q", Publishing{Body: []byte("me")}))
		is.NoErr(pub.Publish("k.what", Publishing{Body: []byte("me")}))
		time.Sleep(time.Millisecond * 10)
		is.NoErr(pub.Close())
		is.NoErr(con.Close())
	}()

	msgs, err := con.Consume(
		"q",
		// "k.*",
		"k.*",
	)
	is.NoErr(err)
	for msg := range msgs {
		msg.Ack(false)
		fmt.Printf("key(%q) body(%q)\n", msg.RoutingKey, msg.Body)
	}
}

func TestScratchpad(t *testing.T) {
	// t.Skip()
	t.Parallel()
	is := is.New(t)
	conn, err := amqp.DialConfig(spec.String(), amqp.Config{
		Heartbeat:  10 * time.Second,
		Locale:     "en_US",
		Properties: amqp.Table{"connection_name": "test-conn"},
	})
	is.NoErr(err)
	defer conn.Close()

	consumer, err := conn.Channel()
	is.NoErr(err)
	defer consumer.Close()
	publisher, err := conn.Channel()
	is.NoErr(err)
	// publisher, err := newChannel(context.Background(), conn)
	// is.NoErr(err)
	defer publisher.Close()
	q, err := consumer.QueueDeclare("test-queue", false, true, false, false, nil)
	is.NoErr(err)

	exchange := "que-test"
	is.NoErr(consumer.ExchangeDeclare(
		exchange, ExchangeTopic,
		true, true, false, false, nil))

	err = consumer.QueueBind(q.Name, "k", exchange, false, nil)
	is.NoErr(err)
	err = consumer.QueueBind(q.Name, "a.*", exchange, false, nil)
	is.NoErr(err)
	// exchange = exchange + "_"

	// Publisher
	go func() {
		err := publisher.Publish(exchange+"", "k", false, false, amqp.Publishing{
			Body: []byte("hello there"),
		})
		is.NoErr(err)
		// time.Sleep(time.Millisecond * 500)
		// fmt.Println("sending second message")
		// consumer.Cancel("test-consumer-one", false)
		err = publisher.Publish(exchange+"", "a.key", false, false, amqp.Publishing{
			Body: []byte("hello there again"),
		})
		is.NoErr(err)
	}()

	ch, err := consumer.Consume(q.Name, "test-consumer-one", true, false, false, false, nil)
	is.NoErr(err)

	// Consumer
	messages := 0
	timeout := time.Millisecond * 750
	notifyCancel := consumer.NotifyCancel(make(chan string))
	for {
		select {
		case <-time.After(timeout):
			goto done
		case cancelMsg := <-notifyCancel:
			fmt.Println("cancel consumer:", cancelMsg)
			// time.Sleep(time.Second)
			goto done
		case msg := <-ch:
			fmt.Printf("key: %q, msg: %s\n", msg.RoutingKey, msg.Body)
			messages++
		}
	}
done:
	// is.True(messages > 0) // should receive more than zero messages
}

type channel struct {
	conn  connection
	ch    *amqp.Channel
	alert notifications

	mu   sync.Mutex
	cond *sync.Cond
	open int32
}

type notifications struct {
	close   chan *amqp.Error
	cancel  chan string
	publish chan amqp.Confirmation
}

type connection interface{ Channel() (*amqp.Channel, error) }

func newChannel(ctx context.Context, conn connection) (*channel, error) {
	var ch = channel{conn: conn}
	ch.cond = sync.NewCond(&ch.mu)
	atomic.StoreInt32(&ch.open, 0)
	go ch.reload(ctx)
	return &ch, nil
}

func (ch *channel) Publish(e, k string, m, i bool, msg amqp.Publishing) error {
	s := time.Now()
	defer func() { fmt.Println("published in", time.Since(s)) }()
	log.WithField("open", atomic.LoadInt32(&ch.open)).Info("publish")
	ch.wait()
	err := ch.ch.Publish(e, k, m, i, msg)
	if err != nil {
		return err
	}
	confirm := <-ch.alert.publish
	fmt.Println(confirm)
	return nil
}

func (ch *channel) reload(ctx context.Context) {
	ch.load()
	for {
		select {
		case <-ctx.Done():
			fmt.Println("closing channel")
			return
		case err, ok := <-ch.alert.close:
			atomic.StoreInt32(&ch.open, 0)
			if !ok {
				time.Sleep(time.Second)
			}
			log.WithError(err).WithField("open", ch.open).Debug("received channel close alert")
			if e := ch.load(); e != nil {
				log.WithError(e).Error("failed to load inner channel")
			}
		}
	}
}

func (ch *channel) Close() error {
	atomic.StoreInt32(&ch.open, 1)
	ch.cond.Broadcast()
	return ch.ch.Close()
}

func (ch *channel) wait() {
	ch.mu.Lock()
	fmt.Println(atomic.LoadInt32(&ch.open))
	for atomic.LoadInt32(&ch.open) == 0 {
		ch.cond.Wait()
	}
	ch.mu.Unlock()
	log.Debug("waiting done")
}

func (ch *channel) load() error {
	if ch.ch != nil {
		ch.ch.Close()
	}
	c, err := ch.conn.Channel()
	if err != nil {
		return err
	}
	log.Debug("inner channel loaded")
	ch.alert.cancel = c.NotifyCancel(make(chan string))
	ch.alert.close = c.NotifyClose(make(chan *amqp.Error))
	ch.alert.publish = c.NotifyPublish(make(chan amqp.Confirmation))
	ch.ch = c
	atomic.StoreInt32(&ch.open, 1)
	ch.cond.Broadcast()
	return nil
}
