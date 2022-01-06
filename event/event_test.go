package event

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/streadway/amqp"
)

func TestBus(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus := NewChannelBus()
	if bus == nil {
		t.Fatal("nil channel bus")
	}
	defer bus.Close()
	con, err := bus.Consumer("q", ConsumeWithContext(ctx), WithPrefetch(3))
	fail(err, t)
	defer con.Close()
	n := 10
	pub, err := bus.Publisher(PublishWithContext(ctx))
	fail(err, t)
	defer pub.Close()
	go func() {
		for i := 0; i < n; i++ {
			err = bus.PublishEvent(
				"q", Event{Body: []byte(fmt.Sprintf("%d", i))})
			handle(err, t)
		}
		time.Sleep(time.Millisecond * 150)
		cancel()
	}()
	consumer(t, nil, con, n)
}

func TestBus_Err(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus := NewChannelBusContext(ctx)
	defer bus.Close()
	con, err := bus.Consumer("1")
	fail(err, t)
	pub, err := bus.Publisher()
	fail(err, t)
	defer pub.Close()
	go func() {
		err = pub.Publish("2", amqp.Publishing{})
		if err == nil {
			t.Error("expected error from publishing on non-existant queue")
		}
		con.Close()
	}()
	consumer(t, nil, con, 0)
}

func TestBusBindKeys(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus := NewChannelBusContext(ctx)
	con, err := bus.Consumer("one", WithKeys("two", "three"))
	fail(err, t)
	defer con.Close()
	pub, err := bus.Publisher()
	fail(err, t)
	n := 5
	go func() {
		for i := 0; i < n; i++ {
			msg := amqp.Publishing{Body: []byte(fmt.Sprintf("%d", i))}
			handle(pub.Publish("one", msg), t)
			handle(pub.Publish("two", msg), t)
			handle(pub.Publish("three", msg), t)
		}
		time.Sleep(time.Millisecond * 25)
		cancel()
	}()
	consumer(t, nil, con, n*3)
}

func TestBusCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bus := NewChannelBusContext(ctx)
	defer bus.Close()
	con, err := bus.Consumer("should-timeout")
	fail(err, t)
	pub, err := bus.Publisher()
	fail(err, t)
	defer pub.Close()

	fail(pub.Publish("should-timeout", amqp.Publishing{}), t)

	go func() {
		<-time.After(time.Millisecond * 50)
		cancel()
		for i := 0; i < 5; i++ {
			fail(pub.Publish("should-timeout", amqp.Publishing{}), t)
		}
		time.Sleep(time.Millisecond * 10)
	}()

	msgs, err := con.Consume()
	fail(err, t)
	count := 0
	for range msgs {
		count++
	}
	if count != 1 {
		t.Errorf("expected 1 receive, not %d", count)
	}
}

type prefetcher struct{ Consumer }
type prefetcht struct{ Consumer }
type qoser struct{ Consumer }

func (prefetcher) WithPrefetch(int) error { return nil }
func (prefetcht) WithPrefetch(int)        {}
func (qoser) Qos(int) error               { return nil }

func TestWithPrefetch(t *testing.T) {
	fail(WithPrefetch(10)(&prefetcher{}), t)
	fail(WithPrefetch(15)(&qoser{}), t)
	fail(WithPrefetch(1)(&prefetcht{}), t)
	err := WithPrefetch(5)(&struct{ Consumer }{})
	if !errors.Is(err, ErrWrongConsumerType) {
		t.Error("expected wrong consumer type error")
	}
	err = WithKeys()(struct{ Consumer }{})
	if err == nil {
		t.Error("expected error")
	}
}

func consumer(t *testing.T, wg *sync.WaitGroup, consumer Consumer, n int) {
	if wg != nil {
		defer wg.Done()
	}
	msgs, err := consumer.Consume()
	if err != nil {
		t.Error(err)
		return
	}
	count := 0
	for msg := range msgs {
		if msg.DeliveryTag != uint64(count) {
			t.Error("wrong consumer tag")
		}
		if err = msg.Ack(false); err != nil {
			t.Error(err)
		}
		count++
		// fmt.Printf("%s\n", msg.Body)
	}
	if count != n {
		t.Errorf("expected to recieve %d messages, actually got %d messages", n, count)
	}
}

func fail(e error, t *testing.T) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}

func handle(e error, t *testing.T) {
	t.Helper()
	if e != nil {
		t.Error(e)
	}
}
