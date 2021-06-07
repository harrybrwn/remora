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

type Crawler struct {
	Visitor Visitor
	Sleep   time.Duration
	Limit   uint
	DB      *badger.DB

	wg    *sync.WaitGroup
	queue chan *PageRequest
	skip  chan *url.URL

	mu       sync.Mutex
	seen     map[string]struct{}
	spiders  map[string]*spider
	finished chan string // channel of finished spider names

	metrics struct {
		sync.Mutex
		stack           int
		vertices, edges int64
	}
}

type Option func(*Crawler)

func WithVisitor(v Visitor) Option          { return func(c *Crawler) { c.Visitor = v } }
func WithSleep(t time.Duration) Option      { return func(c *Crawler) { c.Sleep = t } }
func WithLimit(l uint) Option               { return func(c *Crawler) { c.Limit = l } }
func WithDB(db *badger.DB) Option           { return func(c *Crawler) { c.DB = db } }
func WithQueue(ch chan *PageRequest) Option { return func(c *Crawler) { c.queue = ch } }
func WithSkipChan(ch chan *url.URL) Option  { return func(c *Crawler) { c.skip = ch } }
func WithQueueSize(n int) Option            { return func(c *Crawler) { c.queue = make(chan *PageRequest, n) } }

func NewCrawler(opts ...Option) *Crawler {
	c := &Crawler{
		Limit:    1,
		Visitor:  &NoOpVisitor{},
		wg:       new(sync.WaitGroup),
		seen:     make(map[string]struct{}),
		spiders:  make(map[string]*spider),
		finished: make(chan string),
	}
	for _, o := range opts {
		o(c)
	}
	if c.Visitor == nil {
		c.Visitor = &NoOpVisitor{}
	}
	if c.queue == nil {
		c.queue = make(chan *PageRequest)
	}
	if c.DB == nil {
		// Try not to do this, keeping this in memory will be a nightmare
		opts := badger.DefaultOptions("")
		opts.InMemory = true
		opts.Logger = nil
		c.DB, _ = badger.Open(opts)
	}
	return c
}

func (c *Crawler) Add(a int) { c.wg.Add(a) }
func (c *Crawler) Wait()     { c.wg.Wait() }

func (c *Crawler) Crawl(ctx context.Context, reqLimit int) {
	var (
		err  error
		done context.CancelFunc
		sem  = semaphore.NewWeighted(int64(reqLimit))
	)
	defer c.wg.Done()
	ctx, done = context.WithCancel(ctx)
	defer done()

Loop:
	for {
		select {
		case u := <-c.skip:
			c.MarkVisited(*u)
		case hostname := <-c.finished:
			log.WithField("hostname", hostname).Debug("got finished signal")
			c.mu.Lock()
			err := deleteAndCloseSpider(c.spiders, hostname)
			n := len(c.spiders)
			c.mu.Unlock()
			if err != nil {
				log.WithFields(logrus.Fields{
					"error": err, "hostname": hostname,
				}).Error("could not close spider")
			}
			// all spiders have finished
			if n == 0 {
				return
			}
			log.Tracef("spider %s closed, %d spiders remaining", hostname, n)
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
			case ErrSkipURL:
				continue
			case nil:
				fallthrough
			default:
			}

			go func(p *PageRequest) {
				spider := c.getOrLaunchSpider(ctx, p.URL.Host, sem)
				spider.add(p)
			}(page)
			atomic.AddInt64(&c.metrics.vertices, 1)
		case <-ctx.Done():
			break Loop
		}
	}
}

func (c *Crawler) Enqueue(req *PageRequest) { go func() { c.queue <- req }() }
func (c *Crawler) QueueSize() int           { return len(c.queue) }
func (c *Crawler) SpiderCount() int         { return len(c.spiders) }
func (c *Crawler) VisitedCount() int        { return len(c.seen) }
func (c *Crawler) N() int64                 { return atomic.LoadInt64(&c.metrics.vertices) }

func (c *Crawler) SpiderStats() []*SpiderStats {
	stats := make([]*SpiderStats, 0)
	c.mu.Lock()
	defer c.mu.Unlock()
	for host, spider := range c.spiders {
		spider.mu.Lock()
		stat := &SpiderStats{
			Host:      host,
			WaitTime:  spider.sleep,
			QueueSize: spider.q.Size(),
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

func (c *Crawler) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var e, err error
	for _, spider := range c.spiders {
		e = spider.Close()
		if e != nil && err == nil {
			err = e
		}
	}
	return err
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

func deleteAndCloseSpider(spiders map[string]*spider, hostname string) error {
	spider, ok := spiders[hostname]
	if !ok {
		log.Warning("got finished signal from a non-existant spider")
	}
	delete(spiders, hostname)
	return spider.Close()
}

func (c *Crawler) wasVisited(p *PageRequest) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	seen, err := c.urlSeen(*p.URL)
	if err != nil {
		log.WithError(err).Error("could not access visited url set")
	}
	if seen {
		return true
	}
	if err = c.markvisited(*p.URL); err != nil {
		log.WithError(err).Error("could not access visited url set")
	}
	return false
}

func (c *Crawler) getOrLaunchSpider(ctx context.Context, host string, sem *semaphore.Weighted) *spider {
	c.mu.Lock()
	spider, ok := c.spiders[host]
	c.mu.Unlock()

	if !ok {
		log.Tracef("launching new spider for %s", host)
		spider = c.newSpider(host, sem)
		go func() {
			<-spider.finished
			c.finished <- spider.host
		}()
		c.mu.Lock()
		c.spiders[host] = spider
		c.mu.Unlock()
		spider.wg.Add(1)
		go spider.start(ctx)
	}
	return spider
}

func (c *Crawler) urlSeen(u url.URL) (bool, error) {
	ok := false
	u.Fragment = ""
	// key := u.String()
	// _, ok = c.seen[key]
	// return ok, nil

	key := urlKey(&u)

	err := c.DB.View(func(txn *badger.Txn) error {
		item, err := txn.Get(key)
		if err == badger.ErrKeyNotFound {
			return nil
		}
		if err != nil {
			return err
		}
		if item.IsDeletedOrExpired() {
			return nil
		}
		ok = true
		return nil
	})
	if err != nil {
		return false, err
	}
	return ok, nil
}

func (c *Crawler) markvisited(u url.URL) error {
	u.Fragment = ""
	// key := u.String()
	// c.seen[key] = struct{}{}

	key := urlKey(&u)
	return c.DB.Update(func(txn *badger.Txn) error {
		return txn.Set(key, []byte{1})
	})
}

func urlKey(u *url.URL) []byte {
	s := u.String()
	key := make([]byte, 8, len(s)+8)
	copy(key[:], []byte("visited_"))
	return append(key, []byte(s)...)
}

func (c *Crawler) newSpider(host string, sem *semaphore.Weighted) *spider {
	return &spider{
		visitor:  c.Visitor,
		queue:    c.queue,
		wait:     make(chan time.Duration),
		finished: make(chan struct{}),
		sem:      sem,
		sleep:    c.Sleep,
		wg:       c.wg,
		host:     host,
		ctx:      context.Background(),
		close:    func() {},
		q:        NewPageQueue(c.DB, []byte(host)),
	}
}

type NoOpVisitor struct{}

func (v *NoOpVisitor) Filter(*PageRequest) error { return nil }
func (v *NoOpVisitor) Visit(*Page)               {}
func (v *NoOpVisitor) LinkFound(*url.URL)        {}
