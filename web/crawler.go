package web

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"sync"
)

type Crawler struct {
	pf            Fetcher
	reqCallbacks  []RequestCallback
	pageCallbacks []PageCallback
	linkCallbacks []LinkCallback
	lock          *sync.RWMutex
}

type CrawlerOption func(*Crawler)

func WithFetcher(f Fetcher) CrawlerOption { return func(c *Crawler) { c.pf = f } }

func NewCrawler(opts ...CrawlerOption) *Crawler {
	panic("web.Crawler is not finished")
}

type (
	RequestCallback func(r *Request)
	PageCallback    func(ctx context.Context, page *Page)
	LinkCallback    func(link *url.URL)
)

type Request struct {
	Request *http.Request
}

func (c *Crawler) Init() {
	c.lock = new(sync.RWMutex)
}

func (c *Crawler) OnRequest(fn RequestCallback) {
	c.lock.Lock()
	c.reqCallbacks = append(c.reqCallbacks, fn)
	c.lock.Unlock()
}

func (c *Crawler) OnPage(fn PageCallback) {
	c.lock.Lock()
	c.pageCallbacks = append(c.pageCallbacks, fn)
	c.lock.Unlock()
}

func (c *Crawler) OnLink(fn LinkCallback) {
	c.lock.Lock()
	c.linkCallbacks = append(c.linkCallbacks, fn)
	c.lock.Unlock()
}

func (c *Crawler) Visit(ctx context.Context, URL string) error {
	u, err := url.Parse(URL)
	if err != nil {
		return err
	}
	page, err := c.pf.Fetch(ctx, NewPageRequest(u, 0))
	if err != nil {
		return err
	}
	c.handlePage(ctx, page)
	return nil
}

func (c *Crawler) handlePage(ctx context.Context, page *Page) {
	c.lock.RLock()
	callbacks := slices.Clone(c.pageCallbacks)
	linkCallbacks := slices.Clone(c.linkCallbacks)
	c.lock.RUnlock()
	for _, fn := range linkCallbacks {
		for _, l := range page.Links {
			fn(l)
		}
	}
	for _, fn := range callbacks {
		fn(ctx, page)
	}
}
