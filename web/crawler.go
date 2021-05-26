package web

import (
	"net/url"
	"sync"
)

type Crawler struct {
	wg *sync.WaitGroup

	rwmu sync.RWMutex
	seen map[string]struct{}

	hostlocks struct {
		sync.RWMutex
		m map[string]chan struct{}
	}
}

func NewCrawler() *Crawler {
	c := &Crawler{
		wg:   new(sync.WaitGroup),
		seen: make(map[string]struct{}),
	}
	c.hostlocks.m = make(map[string]chan struct{})
	return c
}

func (c *Crawler) start(base string, urls ...string) error {
	return nil
}

func (c *Crawler) Visited(u url.URL) bool {
	return false
}
