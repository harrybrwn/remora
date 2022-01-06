package frontier

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/matryer/is"
	"github.com/sirupsen/logrus"
	"github.com/streadway/amqp"
)

func TestChannelAutoReload(t *testing.T) {
	log.SetLevel(logrus.DebugLevel)
	log.SetOutput(os.Stdout)
	is := is.New(t)
	u := amqp.URI{
		Scheme:   "amqp",
		Host:     "localhost",
		Port:     5672,
		Username: "guest",
		Password: "guest",
	}
	exchange := "frontier-channel-test"
	conn, err := amqp.DialConfig(u.String(), amqp.Config{
		Heartbeat:  10 * time.Second,
		Locale:     "en_US",
		Properties: amqp.Table{"connection_name": "test-conn"},
	})
	is.NoErr(err)
	done := make(chan struct{})
	defer conn.Close()
	c := channel{
		conn:     conn,
		conndone: done,
	}
	c.chOpen = sync.NewCond(&c.mu)
	c.reload = make(chan struct{})
	c.ctx, c.cancel = context.WithCancel(context.Background())
	c.ch, err = conn.Channel()
	is.NoErr(err)
	go func() { <-c.reload }()
	go func() {
		time.Sleep(time.Second * 2)
		is.NoErr(c.Close())
	}()
	c.wait()
	fmt.Println("hello", exchange)
}
