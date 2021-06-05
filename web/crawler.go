package web

import (
	"context"
	"errors"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dgraph-io/badger/v3"
	"github.com/sirupsen/logrus"
	"golang.org/x/sync/semaphore"
)

var (
	ErrSkipURL = errors.New("skip url")

	log = logrus.StandardLogger()
)

type Visitor interface {
	// Filter is called after checking page depth
	// and after checking for a repeated URL.
	Filter(*Page) error

	// Visit is called after a page is fetched.
	Visit(*Page)

	// LinkFound is called when a new link is
	// found and popped off of the main queue
	// and before any depth checking or repeat
	// checking.
	LinkFound(*url.URL)
}

type Crawler struct {
	Visitor Visitor
	Sleep   time.Duration
	Limit   uint
	DB      *badger.DB

	wg    *sync.WaitGroup
	queue chan *Page
	skip  chan *url.URL

	mu      sync.Mutex
	seen    map[string]struct{}
	spiders map[string]*spider

	metrics struct {
		sync.Mutex
		stack           int
		vertices, edges int64
	}
}

type Option func(*Crawler)

func WithVisitor(v Visitor) Option         { return func(c *Crawler) { c.Visitor = v } }
func WithSleep(t time.Duration) Option     { return func(c *Crawler) { c.Sleep = t } }
func WithLimit(l uint) Option              { return func(c *Crawler) { c.Limit = l } }
func WithDB(db *badger.DB) Option          { return func(c *Crawler) { c.DB = db } }
func WithQueue(ch chan *Page) Option       { return func(c *Crawler) { c.queue = ch } }
func WithSkipChan(ch chan *url.URL) Option { return func(c *Crawler) { c.skip = ch } }
func WithQueueSize(n int) Option           { return func(c *Crawler) { c.queue = make(chan *Page, n) } }

func NewCrawler(opts ...Option) *Crawler {
	c := &Crawler{
		Limit:   1,
		Visitor: &NoOpVisitor{},
		wg:      new(sync.WaitGroup),
		seen:    make(map[string]struct{}),
		spiders: make(map[string]*spider),
	}
	for _, o := range opts {
		o(c)
	}
	if c.Visitor == nil {
		c.Visitor = &NoOpVisitor{}
	}
	if c.queue == nil {
		c.queue = make(chan *Page)
	}
	return c
}

func (c *Crawler) Add(a int) { c.wg.Add(a) }
func (c *Crawler) Wait()     { c.wg.Wait() }

func (c *Crawler) Crawl(ctx context.Context) {
	var (
		err  error
		done context.CancelFunc
	)
	defer c.wg.Done()
	ctx, done = context.WithCancel(ctx)
	defer done()
	if c.Visitor == nil {
		c.Visitor = &NoOpVisitor{}
	}

Loop:
	for {
		select {
		case u := <-c.skip:
			c.MarkVisited(*u)
		case page, ok := <-c.queue:
			if !ok {
				break Loop
			}

			c.Visitor.LinkFound(page.URL)

			// check crawl depth and visited set, insert new URLs if needed
			if page.Depth > c.Limit || c.wasVisited(page) {
				continue
			}

			err = c.Visitor.Filter(page)
			switch err {
			case nil:
				break
			case ErrSkipURL:
				continue
			}

			go func(p *Page) {
				spider := c.getOrLaunchSpider(ctx, p.URL.Host)
				spider.add(p)
			}(page)

			atomic.AddInt64(&c.metrics.vertices, 1)
		case <-ctx.Done():
			break Loop
		}
	}
}

func SetLogger(l *logrus.Logger) { log = l }
func GetLogger() *logrus.Logger  { return log }

func (c *Crawler) Enqueue(page *Page) { go func() { c.queue <- page }() }
func (c *Crawler) QueueSize() int     { return len(c.queue) }
func (c *Crawler) SpiderCount() int   { return len(c.spiders) }
func (c *Crawler) VisitedCount() int  { return len(c.seen) }
func (c *Crawler) N() int64           { return atomic.LoadInt64(&c.metrics.vertices) }

func (c *Crawler) SpiderStats() []*SpiderStats {
	stats := make([]*SpiderStats, 0)
	c.mu.Lock()
	defer c.mu.Unlock()
	for host, spider := range c.spiders {
		spider.mu.Lock()
		stat := &SpiderStats{
			Host:     host,
			WaitTime: spider.sleep,
		}
		stat.PagesFetched = atomic.LoadInt64(&spider.fetched)
		spider.mu.Unlock()
		stats = append(stats, stat)
	}
	return stats
}

func (c *Crawler) Visited(u url.URL) bool {
	u.Fragment = ""
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.seen[u.String()]
	return ok
}

func (c *Crawler) MarkVisited(u url.URL) {
	u.Fragment = ""
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seen[u.String()] = struct{}{}
}

func (c *Crawler) SetSleep(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Sleep = d
	for _, spider := range c.spiders {
		// time.Duration is just an int64
		atomic.StoreInt64((*int64)(&spider.sleep), int64(d))
	}
}

func (c *Crawler) CloseSpider(host string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	spider, ok := c.spiders[host]
	if !ok {
		return errors.New("no spider for host")
	}
	return spider.Close()
}

func (c *Crawler) wasVisited(p *Page) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.urlSeen(*p.URL) {
		return true
	}
	if p.Redirected {
		c.markvisited(*p.RedirectedFrom) // mark old url as visited
	}
	c.markvisited(*p.URL)
	return false
}

func (c *Crawler) getOrLaunchSpider(ctx context.Context, host string) *spider {
	c.mu.Lock()
	spider, ok := c.spiders[host]
	c.mu.Unlock()

	if !ok {
		log.Tracef("launching new spider for %s", host)
		spider = c.newSpider(host, 100)
		c.mu.Lock()
		c.spiders[host] = spider
		c.mu.Unlock()
		spider.wg.Add(1)
		go spider.start(ctx)
	}
	return spider
}

func (c *Crawler) urlSeen(u url.URL) bool {
	u.Fragment = ""
	key := u.String()
	_, ok := c.seen[key]
	return ok
}

func (c *Crawler) markvisited(u url.URL) {
	u.Fragment = ""
	key := u.String()
	c.seen[key] = struct{}{}
}

func (c *Crawler) newSpider(host string, fetchLimit int64) *spider {
	return &spider{
		visitor: c.Visitor,
		queue:   c.queue,
		sem:     semaphore.NewWeighted(fetchLimit),
		sleep:   c.Sleep,
		wg:      c.wg,
		host:    host,
		ctx:     context.Background(),
		close:   func() {},
		q:       NewPageQueue(c.DB),
	}
}

type NoOpVisitor struct{}

func (v *NoOpVisitor) Filter(*Page) error { return nil }
func (v *NoOpVisitor) Visit(*Page)        {}
func (v *NoOpVisitor) LinkFound(*url.URL) {}
