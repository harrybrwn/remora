package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"sync"
	"time"

	"github.com/streadway/amqp"
	"golang.org/x/sync/semaphore"
)

func NewCollector(crawLimit uint, weight int64) *pageCollector {
	const size = 1000
	// size := weight
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
	stats sync.Mutex
	stack int
	files int
}

func (pc *pageCollector) WaitAndClose() {
	pc.wg.Wait()
	// close(pc.pages)
	close(pc.errs)
}

func (pc *pageCollector) collect(ctx context.Context, page *Page, ch *amqp.Channel) (err error) {
	defer func() {
		pc.sem.Release(1)
		pc.wg.Done()
	}()

	err = page.Fetch()
	if err != nil {
		pc.errs <- err
		return err
	}

	if page.redirected {
		pc.markVisited(*page.URL)
	}
	pc.wg.Add(1)
	go pc.asyncEnqueueLinks(ctx, page, ch)
	return nil
}

func (pc *pageCollector) asyncEnqueueLinks(ctx context.Context, page *Page, ch *amqp.Channel) {
	pc.stats.Lock()
	pc.stack++
	pc.stats.Unlock()
	defer func() {
		pc.stats.Lock()
		pc.stack--
		pc.stats.Unlock()
	}()
	defer pc.wg.Done()
	for _, l := range page.Links {
		if pc.visited(*l) {
			continue
		}
		if l.Host != page.URL.Host {
			continue
		}
		if !validURLScheme(l.Scheme) {
			continue
		}

		p := PageMsg{URL: l.String(), Depth: page.Depth + 1}
		raw, err := json.Marshal(p)
		if err != nil {
			pc.errs <- err
			continue
		}
		err = ch.Publish("", "diktyo-queue", false, false, amqp.Publishing{
			Type:         "page",
			Body:         raw,
			Timestamp:    time.Now(),
			ContentType:  "application/json",
			DeliveryMode: 2,
		})
		if err != nil {
			pc.errs <- err
		}
	}
}

func (pc *pageCollector) _collect(page *Page) error {
	defer func() {
		pc.sem.Release(1)
		pc.wg.Done()
	}()
	pc.stats.Lock()
	pc.stack++
	pc.stats.Unlock()
	defer func() {
		pc.stats.Lock()
		pc.stack--
		pc.stats.Unlock()
	}()

	if page.Depth > pc.limit {
		return nil
	}
	// if this page has been visited we stop,
	// otherwise mark the current page as visited
	if pc.tryMarkVisited(*page.URL) {
		return nil
	}

	n := 0
	defer func() { pc.stats.Lock(); pc.files -= n; pc.stats.Unlock() }()

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

		p := NewPage(l, page.Depth+1)
		if p.Depth > pc.limit {
			continue
		}

		pc.stats.Lock()
		pc.files++
		n++
		pc.stats.Unlock()
		err := p.Fetch()
		if err != nil {
			pc.errs <- err
			continue
		}

		// If the request was redirected we don't want
		// to follow the path in any subsequent traversals
		if p.redirected {
			log.Info(l.String(), " redirected to ", p.URL.String())
			pc.markVisited(*l)

			// If the link redirected to a different url then
			// the we also need to check if we have been to the
			// redirect location as well as the original link.
			if pc.visited(*p.URL) {
				continue
			}
		}

		select {
		case pc.pages <- p:
		default:
			pc.errs <- &fullQueueError{l.String()}
			// this happens a lot, the return limits
			// the number of "full queue" errors but should be changed in the future
			return nil
		}
	}
	return nil
}

type fullQueueError struct {
	url string
}

func (qfe *fullQueueError) Error() string {
	return fmt.Sprintf("queue full: skipping %s", qfe.url)
}

func (pc *pageCollector) visited(u url.URL) bool {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	if u.Path == "" && u.Fragment != "" {
		u.Fragment = ""
	}
	_, ok := pc.set[u.String()]
	return ok
}

func (pc *pageCollector) markNotVisited(u url.URL) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	if u.Fragment != "" {
		u.Fragment = ""
	}
	delete(pc.set, u.String())
}

func (pc *pageCollector) markVisited(u url.URL) {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	if u.Fragment != "" {
		u.Fragment = ""
	}
	pc.set[u.String()] = struct{}{}
}

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

func (pc *pageCollector) writeVisitedSet(w io.Writer) (err error) {
	for key := range pc.set {
		_, err = fmt.Fprintf(w, "%s\n", key)
		if err != nil {
			return err
		}
	}
	return nil
}
