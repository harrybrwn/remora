package main

import (
	"fmt"
	"net/url"
	"sync"

	"golang.org/x/sync/semaphore"
)

func NewCollector(crawLimit uint, weight int64) *pageCollector {
	const size = 5000
	return &pageCollector{
		limit: crawLimit,
		pages: make(chan *Page, size),
		errs:  make(chan error, size),
		sem:   semaphore.NewWeighted(weight),
		set:   make(map[string]struct{}),
	}
}

type pageCollector struct {
	wg sync.WaitGroup

	pages chan *Page
	errs  chan error
	limit uint
	sem   *semaphore.Weighted

	mu  sync.Mutex
	set map[string]struct{}

	// misc stats
	stack int
}

func (pc *pageCollector) WaitAndClose() {
	pc.wg.Wait()
	close(pc.pages)
}

func (pc *pageCollector) collect(page *Page) error {
	defer func() {
		pc.sem.Release(1)
		pc.wg.Done()
	}()

	pc.mu.Lock()
	pc.stack++
	pc.mu.Unlock()
	defer func() { pc.mu.Lock(); pc.stack--; pc.mu.Unlock() }()

	if pc.tryMarkVisited(page.URL) {
		return nil
	}
	if page.depth > pc.limit {
		return nil
	}

	for _, l := range page.Links {
		if pc.visited(*l) {
			continue
		}

		// TODO Remove this
		// this will keep the graph traversal on one
		// host which is very limiting on small websites
		if l.Host != page.URL.Host {
			continue
		}

		if !validURLScheme(l.Scheme) {
			continue
		}
		if len(l.Path) >= 2 && l.Path[0:2] == "./" {
			continue
		}

		p := &Page{URL: *l, depth: page.depth + 1}
		if p.depth > pc.limit {
			continue
		}

		err := p.Fetch()
		if err != nil {
			// pc.errs <- &urlError{URL: l, err: err}
			pc.errs <- err
			continue
		}
		// If the link redirected to a different url then then
		// the we also need to check if we have been to the
		// redirect location as well as the original link.
		if pc.visited(p.URL) {
			continue
		}

		select {
		case pc.pages <- p:
		default:
			fmt.Printf("\rskipping %v\n", page.URL)
			return nil
		}
	}
	return nil
}

// type urlError struct {
// 	URL *url.URL
// 	err error
// }

// func (e *urlError) Error() string {
// 	return fmt.Sprintf("%v: %v", e.err, e.URL)
// }

func (pc *pageCollector) visited(u url.URL) bool {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	if u.Path == "" && u.Fragment != "" {
		u.Fragment = ""
	}
	_, ok := pc.set[u.String()]
	return ok
}

// func (pc *pageCollector) markNotVisited(u url.URL) {
// 	pc.mu.Lock()
// 	defer pc.mu.Unlock()
// 	if u.Path == "" && u.Fragment != "" {
// 		u.Fragment = ""
// 	}
// 	delete(pc.set, u.String())
// }

func (pc *pageCollector) tryMarkVisited(link url.URL) bool {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	if link.Path == "" && link.Fragment != "" {
		link.Fragment = ""
	}
	_, ok := pc.set[link.String()]

	if ok {
		return true
	}
	pc.set[link.String()] = struct{}{}
	return false
}
