package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"sync"
	"testing"
	"time"

	"github.com/harrybrwn/diktyo/event"
	"github.com/harrybrwn/diktyo/internal/visitor"
	"github.com/harrybrwn/diktyo/storage"
	"github.com/harrybrwn/diktyo/web"
	"github.com/sirupsen/logrus"
	"github.com/streadway/amqp"
	"google.golang.org/protobuf/proto"
)

func Test(t *testing.T) {
}

func TestRedirects(t *testing.T) {
	log.SetFormatter(&logrus.JSONFormatter{})
	log.SetOutput(os.Stdout)
	log.SetLevel(logrus.DebugLevel)
	var wg sync.WaitGroup
	links := []string{
		"https://en.wikipedia.org/",
		"https://loc.gov/help/",
		"https://creativecommons.org/legalcode",
		"http://foundation.wikimedia.org/wiki/Terms_of_Use",
		"http://quotes.toscrape.com/",
	}
	wg.Add(len(links))
	for _, l := range links {
		go func(l string) {
			defer wg.Done()
			u, err := url.Parse(l)
			if err != nil {
				panic(err)
			}
			p := web.NewPage(u, 0)

			err = p.Fetch()
			if err != nil {
				t.Error(err)
				return
			}
			if p == nil {
				t.Error("nil page")
				return
			}
			fmt.Println(u)
			fmt.Println(p.URL)
			fmt.Println("redirected", p.URL.Path != u.Path)
			println()
		}(l)
	}
	wg.Wait()
}

type vis struct{}

func (*vis) Filter(*web.PageRequest, *url.URL) error { return nil }
func (*vis) Visit(_ context.Context, p *web.Page)    { log.Infof("visit %s", p.URL.String()) }
func (*vis) LinkFound(*url.URL)                      {}

func TestSpider(t *testing.T) {
	log.SetLevel(logrus.TraceLevel)
	// log.SetLevel(logrus.InfoLevel)
	// log.SetLevel(logrus.PanicLevel)
	web.SetLogger(log)
	visitor.SetLogger(log)
	ctx := context.Background()
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()
	// opts := badger.DefaultOptions("")
	// opts.Logger = nil
	// opts.InMemory = true
	// db, err := badger.Open(opts)
	// if err != nil {
	// 	t.Fatal(err)
	// }
	// bus := eventqueue.New(db)
	bus := event.NewChannelBus()
	defer func() {
		// close bus before db
		bus.Close()
		// db.Close()
	}()
	req := web.PageRequest{
		URL:   "https://en.wikipedia.org/wiki/Main_Page",
		Depth: 0,
	}
	raw, err := proto.Marshal(&req)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		<-time.After(time.Second * 2)
		pub, _ := bus.Publisher()
		log.Info("publishing seed url")
		err = pub.Publish("en.wikipedia.org", amqp.Publishing{Body: raw})
		if err != nil {
			t.Error(err)
			cancel()
		}
	}()

	ctx, cancel = context.WithTimeout(ctx, time.Second*5)
	defer cancel()
	s := &spider{
		Wait:    time.Millisecond * 500 * 2 * 10,
		Host:    "en.wikipedia.org",
		Visitor: &vis{},
		URLSet:  storage.NewInMemoryURLSet(),
		Bus:     bus,
	}
	go func() {
		err = s.start(ctx)
		if err != nil {
			t.Error(err)
			cancel()
		}
	}()
	<-ctx.Done()
}
