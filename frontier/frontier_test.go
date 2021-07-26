package frontier

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"testing"
	"time"

	"github.com/harrybrwn/diktyo/event"
	"github.com/sirupsen/logrus"
	"github.com/streadway/amqp"
)

const (
	queuename = "test"

	exchange  = "logs"
	consumers = 5
)

var testexchange = Exchange{
	Name:       "logs",
	Kind:       DefaultExchangeKind,
	Durable:    true,
	AutoDelete: false,
}

// var (
// 	_ EventBus  = (*Frontier)(nil)
// 	_ Consumer  = (*consumer)(nil)
// 	_ Publisher = (*publisher)(nil)
// )

func Test(t *testing.T) {}

func TestChannelReload(t *testing.T) {
	log.SetLevel(logrus.TraceLevel)
	conn, err := amqp.DialConfig(
		uri(Connect{Host: "localhost", Port: 5672}),
		amqp.Config{Heartbeat: 10 * time.Second, Locale: "en_US"},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var (
		connClose   = make(chan *amqp.Error)
		connBlocked = make(chan amqp.Blocking)
	)
	conn.NotifyClose(connClose)
	conn.NotifyBlocked(connBlocked)
	publisher, err := conn.Channel()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	ch := &channel{
		conn:     conn,
		conndone: done,
	}
	initChannel(context.TODO(), ch)

	publisher.QueueDeclare("q", true, false, false, false, nil)
	delivery, err := ch.Consume("q", "", true, false, false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		<-time.After(time.Second * 2)
		ch.ch.Close()
		println("closed inner channel")

		<-time.After(time.Second * 2)
		ch.Reload()
	}()

	const messages = 20
	go func() {
		for i := 0; i < messages; i++ {
			err := publisher.Publish("", "q", false, false, amqp.Publishing{
				Body:         []byte(fmt.Sprintf("message %d", i)),
				DeliveryMode: amqp.Transient,
			})
			if err != nil {
				t.Error(err)
			}
			time.Sleep(time.Millisecond * 500)
		}
		ch.Close()
		publisher.QueueDelete("q", false, false, false)
		publisher.Close()
	}()

	count := 0
Loop:
	for {
		select {
		case msg, ok := <-delivery:
			if !ok {
				break Loop
			}
			fmt.Printf("msg: %s\n", msg.Body)
			count++
		case err := <-connClose:
			fmt.Println("connection closed:", err)
			break Loop
		case blocked := <-connBlocked:
			fmt.Println("connection blocked:", blocked)
			break Loop
		}
	}
	if count != messages {
		t.Errorf("wrong number of messages received: got %d, want %d", count, messages)
	}
}

var (
	mu     sync.Mutex
	queues = 0
)

func consume(t *testing.T, ch *amqp.Channel, exchange string, keys ...string) {
	mu.Lock()
	id := queues
	queues++
	mu.Unlock()

	q, err := ch.QueueDeclare(
		// fmt.Sprintf("%s.%s", queuename, key),
		fmt.Sprintf("%s-%d", queuename, id),
		// queuename,
		true,  // durable
		true,  // autodelete
		false, // exclusive - delete when the connection that declared it closes
		false, // no wait
		nil,
	)
	if err != nil {
		t.Errorf("could not delare queue: %v", err)
		mu.Unlock()
		return
	}
	err = exchangeDeclare(ch)
	if err != nil {
		t.Errorf("could not declare exchange: %v", err)
		return
	}
	for _, key := range keys {
		if err = ch.QueueBind(
			q.Name,
			key,
			exchange,
			false,
			nil,
		); err != nil {
			t.Error(err)
		}
	}

	fmt.Printf("bound keys %v to queue %q\n", keys, q.Name)
	err = ch.Qos(1, 0, false)
	if err != nil {
		t.Error(err)
	}
	delivery, err := ch.Consume(
		q.Name,
		"",
		true,  // auto ack
		false, // exclusive
		false, // no local
		false, // no wait
		nil,
	)
	if err != nil {
		t.Errorf("could not start consumer: %v", err)
		return
	}
	for msg := range delivery {
		fmt.Printf("[%v %q %q] %v %v => %s\n", keys[0], msg.Exchange, msg.RoutingKey, msg.ConsumerTag, msg.DeliveryTag, msg.Body)
		// time.Sleep(time.Second * 2)
	}
}

func exchangeDeclare(ch *amqp.Channel) error {
	// e := Exchange{
	// 	Name:       exchange,
	// 	Kind:       DefaultExchangeKind,
	// 	Durable:    true,
	// 	AutoDelete: true,
	// }
	// return e.declare(ch)
	return testexchange.declare(ch)
}

func TestConsumerPlain(t *testing.T) {
	// This is a consumer that does not use the frontier abstractions
	// for creating consumers.
	t.Skip()
	signals := make(chan os.Signal, 5)
	signal.Notify(signals, os.Interrupt)
	ctx, cancel := context.WithCancel(context.Background())
	var (
		front = &Frontier{Exchange: Exchange{
			Name: exchange, Durable: true, AutoDelete: true}}
	)
	err := front.Connect(ctx, Connect{Host: "localhost", Port: 5672})
	if err != nil {
		t.Fatal(err)
	}

	var channels [consumers]*amqp.Channel
	for i := 0; i < consumers; i++ {
		channels[i], err = front.conn.Channel()
		if err != nil {
			t.Error(err)
			continue
		}
		// go consume(t, channels[i], exchange, fmt.Sprintf("log.testing.%d", i))
	}
	<-signals

	for _, ch := range channels {
		if err = ch.Close(); err != nil {
			t.Error(err)
		}
	}
	cancel()
}

func TestConsumer(t *testing.T) {
	t.Parallel()
	logrus.SetLevel(logrus.DebugLevel)
	signals := make(chan os.Signal, 5)
	signal.Notify(signals, os.Interrupt)
	timeout := time.Second * 2
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	var (
		front = &Frontier{Exchange: testexchange}
	)
	err := front.Connect(ctx, Connect{Host: "localhost", Port: 5672})
	if err != nil {
		t.Fatal(err)
	}
	defer front.Close()

	// go func() {
	// 	time.Sleep(time.Second * 3)
	// 	front.conn.Close()
	// }()

	defer cancel()
	c, err := front.Consumer(
		queuename,
		WithAutoAck(true),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// deliveries, err := c.Consume("log.*.*")
	// if err != nil {
	// 	t.Fatal(err)
	// }
	// go func() {
	// 	for {
	// 		select {
	// 		case <-ctx.Done():
	// 			fmt.Println("context canceled")
	// 			return
	// 		case msg, ok := <-deliveries:
	// 			if !ok {
	// 				fmt.Println("deliveries channel closed")
	// 				return
	// 			}
	// 			if msg.Body == nil {
	// 				t.Error("got nil message body")
	// 				return
	// 			}
	// 			fmt.Printf("[%q %q] %v %v => %s\n", msg.Exchange, msg.RoutingKey, msg.ConsumerTag, msg.DeliveryTag, msg.Body)
	// 		}
	// 	}
	// }()

	for i := 0; i < consumers; i++ {
		c, err := front.Consumer(
			queuename,
			WithAutoAck(true),
			WithPrefetch(5),
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
	var (
		front = &Frontier{Exchange: testexchange}
		wait  = time.Millisecond * 10
	)
	wait = time.Millisecond * 250
	wait = time.Millisecond * 100
	wait = time.Millisecond
	err := front.Connect(
		context.Background(),
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
			// fmt.Println("msg", i, "published to", key)
		case <-done:
			return
		}
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
