package web

import (
	"context"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
)

type vis struct {
	filter    func(*Page) error
	visit     func(*Page)
	linkFound func(*url.URL)
}

func (v *vis) Filter(p *Page) error { return v.filter(p) }
func (v *vis) Visit(p *Page)        { v.visit(p) }
func (v *vis) LinkFound(u *url.URL) { v.linkFound(u) }
func newvis() *vis {
	return &vis{
		filter:    func(p *Page) error { return nil },
		visit:     func(p *Page) {},
		linkFound: func(u *url.URL) {},
	}
}

func TestCrawler(t *testing.T) {
	var (
		v = newvis()
		n int64
	)
	log.SetLevel(logrus.FatalLevel)
	v.filter = func(p *Page) error { return nil }
	v.visit = func(p *Page) { atomic.AddInt64(&n, 1) }
	var (
		ctx     = context.Background()
		db      = inMemDB(t)
		crawler = NewCrawler(
			WithDB(db),
			WithLimit(2),
			WithQueueSize(500),
			WithSleep(0),
			WithVisitor(v),
		)
	)
	defer crawler.Close()
	p := NewPageFromString("https://quotes.toscrape.com/", 0)
	crawler.Enqueue(p)

	ctx, cancel := context.WithTimeout(ctx, time.Second*2)
	defer cancel()
	crawler.wg.Add(1)
	go crawler.Crawl(ctx, 10)
	<-ctx.Done()
}
