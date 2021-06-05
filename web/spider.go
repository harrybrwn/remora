package web

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dgraph-io/badger/v3"
	"github.com/harrybrwn/diktyo/queue"
	"github.com/sirupsen/logrus"
	"golang.org/x/sync/semaphore"
)

type spider struct {
	queue chan<- *Page
	sleep time.Duration

	wg *sync.WaitGroup
	// Semaphore that limits the number of
	// concurrent page fetches.
	sem   *semaphore.Weighted
	mu    sync.Mutex
	ctx   context.Context
	close context.CancelFunc

	visitor Visitor
	fetched int64
	host    string

	q PageQueue
}

func (s *spider) Close() error {
	s.close()
	return nil
}

func (s *spider) start(ctx context.Context) {
	defer s.wg.Done()
	defer log.Infof("stopping spider %s", s.host)
	s.withContext(ctx)
	ctx = s.ctx

	for {
		select {
		case <-s.ctx.Done():
			return
		default:
		}
		page, err := s.q.Dequeue()
		if err != nil {
			log.WithFields(logrus.Fields{
				"error": err, "spider": s.host,
			}).Error("could not pop from queue")
			break
		}
		s.sem.Acquire(ctx, 1)
		go s.fetch(ctx, page)
		time.Sleep(s.sleep)
	}
}

func (s *spider) fetch(ctx context.Context, p *Page) {
	defer s.sem.Release(1)
	err := p.FetchCtx(ctx)
	s.visitor.Visit(p)
	atomic.AddInt64(&s.fetched, 1)
	if err != nil {
		log.WithFields(logrus.Fields{
			"type": fmt.Sprintf("%T", err),
			"url":  p.URL.String(),
		}).Error(err)
		return
	}
	pushlinks(ctx, &s.mu, s.queue, p)
}

func (s *spider) add(p *Page) {
	err := s.q.Enqueue(p)
	if err != nil {
		log.WithFields(logrus.Fields{
			"error":  err,
			"spider": s.host,
			"url":    p.URL.String(),
		}).Error("could not enqueue page")
	}
}

func addPageToQueue(q chan []*Page, p *Page) {
	pageLinks := []*Page{p}
	for {
		select {
		// Try to send the links
		case q <- pageLinks:
			return
		// If the channel is full then we get the array
		// stored in the channel and we append the new links
		// and send on the next loop iteration. Effectively
		// an infinitely buffered channel.
		case buf := <-q:
			pageLinks = append(buf, pageLinks...)
		}
	}
}

func (s *spider) withContext(ctx context.Context) {
	cloned, close := context.WithCancel(ctx)
	s.ctx = cloned
	s.close = close
}

// PageQueue is a queue for pages
type PageQueue interface {
	Enqueue(*Page) error
	Dequeue() (*Page, error)
}

func NewPageQueue(db *badger.DB) PageQueue {
	return &pageQueue{q: queue.New(db)}
}

type pageQueue struct {
	q queue.Queue
}

func (q *pageQueue) Enqueue(p *Page) error {
	raw, err := json.Marshal(p)
	if err != nil {
		return err
	}
	key := []byte(p.URL.String())
	return q.q.PutKey(key, raw)
}

func (q *pageQueue) Dequeue() (*Page, error) {
	p := &Page{}
	raw, err := q.q.Pop()
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(raw, p); err != nil {
		return nil, err
	}
	return p, nil
}

type SpiderStats struct {
	PagesFetched int64
	Host         string
	WaitTime     time.Duration
}

func pushlinks(
	ctx context.Context,
	lock sync.Locker,
	queue chan<- *Page,
	parent *Page,
) {
	lock.Lock()
	d := parent.Depth
	lock.Unlock()
	for _, l := range parent.Links {
		p := NewPage(l, d+1)
		select {
		case queue <- p:
		case <-ctx.Done():
			return
		default:
		}
	}
}
