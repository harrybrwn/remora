package web

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dgraph-io/badger/v3"
	"github.com/harrybrwn/diktyo/internal"
	"github.com/harrybrwn/diktyo/queue"
	"github.com/sirupsen/logrus"
	"github.com/temoto/robotstxt"
	"golang.org/x/sync/semaphore"
	"google.golang.org/protobuf/proto"
)

var RetryLimit int32 = 5

type spider struct {
	queue chan<- *PageRequest
	q     RequestQueue
	sleep time.Duration

	// Semaphore that limits the number of
	// concurrent page fetches.
	sem *semaphore.Weighted

	mu sync.Mutex
	wg *sync.WaitGroup

	robots *robotstxt.RobotsData

	ctx      context.Context
	close    context.CancelFunc
	wait     chan time.Duration
	finished chan struct{}

	visitor Visitor
	fetched int64
	host    string
}

func (s *spider) Close() error {
	s.close()
	return s.q.Close()
}

func (s *spider) Finished() <-chan struct{} {
	return s.finished
}

func (s *spider) start(ctx context.Context) {
	s.withContext(ctx)
	ctx = s.ctx
	if s.sleep == 0 {
		s.sleep = time.Microsecond
	}
	var (
		err  error
		tick = time.NewTicker(s.sleep)
	)
	s.robots, err = GetRobotsTxT(s.host)
	if err != nil {
		s.robots = &robotstxt.RobotsData{}
		log.WithError(err).Error("could not get robots.txt")
	}
	for _, sitemap := range s.robots.Sitemaps {
		log.Infof("sitemap %s", sitemap)
	}

	defer func() {
		s.wg.Done()
		log.Infof("stopping spider %s", s.host)
		close(s.finished)
		tick.Stop()
	}()

	for {
		req, err := s.q.Dequeue()
		if err == queue.ErrQueueClosed {
			return
		}
		if err != nil {
			log.WithFields(logrus.Fields{
				"error": err, "host": s.host, "q-size": s.q.Size(),
			}).Error("could not pop from queue")
			continue
		}
		page := NewPageFromString(req.URL, req.Depth)
		if !s.robots.TestAgent(page.URL.Path, "*") {
			log.WithFields(logrus.Fields{
				"url": req.URL,
			}).Info("skipping path in robots.txt")
			continue
		}
		s.sem.Acquire(ctx, 1)
		go s.fetch(s.ctx, req, page)

		// wait for either a cancellation or ticker
		select {
		case <-tick.C:
			tick.Reset(s.sleep)
		case <-ctx.Done():
			return
		case wait := <-s.wait:
			time.Sleep(wait)
		}
	}
}

func (s *spider) fetch(ctx context.Context, req *PageRequest, page *Page) {
	err := page.FetchCtx(ctx)
	if err != nil {
		// Don't release the semaphore if the context was cancelled
		// it should handle that itself.
		switch internal.UnwrapAll(err) {
		case context.DeadlineExceeded, context.Canceled:
			return
		default:
			s.sem.Release(1)
		}
		log.WithFields(logrus.Fields{
			"type": fmt.Sprintf("%T", err),
			"url":  req.URL,
		}).Error(err)
		return
	}

	if page.Status == http.StatusTooManyRequests && req.Retry < RetryLimit {
		go func() { s.wait <- page.RetryAfter }()
		req.Retry++
		err = s.q.Enqueue(req)
		if err != nil {
			log.WithError(err).Error("could not enqueue retry")
		}
		s.sem.Release(1)
		return
	}

	defer s.sem.Release(1)
	atomic.AddInt64(&s.fetched, 1)
	d := page.Depth
	for _, l := range page.Links {
		p := NewPageRequest(l, d+1)
		select {
		case s.queue <- p:
		case <-ctx.Done():
			return
		}
	}
	s.visitor.Visit(ctx, page)
}

func (s *spider) add(p *PageRequest) {
	err := s.q.Enqueue(p)
	if err != nil {
		log.WithFields(logrus.Fields{
			"error":  err,
			"spider": s.host,
			"url":    p.URL,
		}).Error("could not enqueue page")
	}
}

func (s *spider) withContext(ctx context.Context) {
	cloned, close := context.WithCancel(ctx)
	s.ctx = cloned
	s.close = close
}

func NewPageQueue(db *badger.DB, prefix []byte) RequestQueue {
	return &pageQueue{queue.New(db, prefix)}
}

type pageQueue struct{ queue.Queue }

func (q *pageQueue) Enqueue(p *PageRequest) error {
	raw, err := proto.Marshal(p)
	if err != nil {
		return err
	}
	key := []byte(p.URL)
	return q.PutKey(key, raw)
}

func (q *pageQueue) Dequeue() (*PageRequest, error) {
	p := &PageRequest{}
	raw, err := q.Pop()
	if err != nil {
		return nil, err
	}
	if err = proto.Unmarshal(raw, p); err != nil {
		return nil, err
	}
	return p, nil
}

type SpiderStats struct {
	PagesFetched int64
	QueueSize    int64
	Host         string
	WaitTime     time.Duration
}
