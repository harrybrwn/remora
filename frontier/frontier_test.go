package frontier

import (
	"fmt"
	"os"
	"os/signal"
	"sync"
	"testing"
	"time"

	"github.com/harrybrwn/diktyo/cmd"
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
	AutoDelete: true,
}

var (
	_ Queue     = (*Frontier)(nil)
	_ Consumer  = (*consumer)(nil)
	_ Publisher = (*publisher)(nil)
)

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

func TestConsumer(t *testing.T) {
	signals := make(chan os.Signal, 5)
	signal.Notify(signals, os.Interrupt)
	var (
		config = cmd.MessageQueueConfig{Host: "localhost", Port: 5672}
		front  = &Frontier{Exchange: Exchange{Name: exchange, Durable: true, AutoDelete: true}}
	)
	err := front.Connect(config.URI())
	if err != nil {
		t.Fatal(err)
	}

	var channels [consumers]*amqp.Channel
	// err = exchangeDeclare(ch)
	// if err != nil {
	// 	t.Fatalf("could not declare exchange: %v", err)
	// }
	for i := 0; i < consumers; i++ {
		channels[i], err = front.conn.Channel()
		if err != nil {
			t.Error(err)
			continue
		}
		go consume(t, channels[i], exchange, fmt.Sprintf("log.testing.%d", i))
	}
	// go consumer(t, front.ch, exchange, "test.10", "test11", "test.12")
	<-signals

	for _, ch := range channels {
		if err = ch.Close(); err != nil {
			t.Error(err)
		}
	}
}

func TestConsumer2(t *testing.T) {
	signals := make(chan os.Signal, 5)
	signal.Notify(signals, os.Interrupt)
	var (
		config = cmd.MessageQueueConfig{Host: "localhost", Port: 5672}
		front  = &Frontier{Exchange: testexchange}
	)
	err := front.Connect(config.URI())
	if err != nil {
		t.Fatal(err)
	}

	c, err := front.Consumer(
		queuename,
		WithAutoAck(true),
		// WithKeys("log.testing.0"),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	deliveries, err := c.Consume("log.*.*")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for msg := range deliveries {
			fmt.Printf("[%q %q] %v %v => %s\n", msg.Exchange, msg.RoutingKey, msg.ConsumerTag, msg.DeliveryTag, msg.Body)
		}
	}()

	// for i := 0; i < consumers; i++ {
	// 	c, err := front.Consumer(
	// 		queuename,
	// 		WithAutoAck(true),
	// 		WithPrefetch(5),
	// 	)
	// 	if err != nil {
	// 		t.Fatal(err)
	// 	}
	// 	defer c.Close()
	// 	go func(c Consumer, id int) {
	// 		deliveries, err := c.Consume(fmt.Sprintf("log.testing.%d", id))
	// 		if err != nil {
	// 			log.Println(err)
	// 			return
	// 		}
	// 		for msg := range deliveries {
	// 			fmt.Printf("[%q %q] %v %v => %s\n", msg.Exchange, msg.RoutingKey, msg.ConsumerTag, msg.DeliveryTag, msg.Body)
	// 		}
	// 	}(c, i)
	// }
	<-signals
}

func TestPublisher(t *testing.T) {
	var (
		config = cmd.MessageQueueConfig{Host: "localhost", Port: 5672}
		front  = &Frontier{Exchange: testexchange}
	)
	err := front.Connect(config.URI())
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
		tick = time.NewTicker(time.Millisecond * 100)
	)
	defer func() {
		tick.Stop()
		close(done)
	}()
	for i := 0; i < 10*consumers; i++ {
		select {
		case <-tick.C:
			key := fmt.Sprintf("log.%[1]d.%[1]d", i%consumers)
			// key = ""
			err = pub.Publish(key, amqp.Publishing{
				DeliveryMode: amqp.Transient,
				Body:         []byte(fmt.Sprintf("hello from tick %d", i)),
			})
			if err != nil {
				t.Error(err)
				break
			}
			fmt.Println("msg", i, "published to", key)
		case <-done:
			return
		}
	}
}
