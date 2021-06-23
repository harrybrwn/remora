package visitor

import (
	"bytes"
	"context"
	"database/sql"
	sqldriver "database/sql/driver"
	"encoding/hex"
	"fmt"
	"hash"
	"hash/fnv"
	"io"
	"net/url"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/harrybrwn/diktyo/internal"
	"github.com/harrybrwn/diktyo/web"
	"github.com/lib/pq" // database driver
	"github.com/sirupsen/logrus"
)

const Timed = false

var (
	log *logrus.Logger = logrus.New()

	hostname string
)

func init() {
	var err error
	hostname, err = os.Hostname()
	if err != nil {
		log.Warning("could not get hostname")
	}
}

func SetLogger(l *logrus.Logger) { log = l }

func New(db *sql.DB) *Visitor {
	return &Visitor{
		db:    db,
		hosts: make(map[string]struct{}),
	}
}

func AddHost(v web.Visitor, host ...string) {
	AddHosts(v, host)
}

func AddHosts(v web.Visitor, hosts []string) {
	vis, ok := v.(*Visitor)
	if !ok {
		panic("cannot add hosts to this visitor")
	}

	for _, h := range hosts {
		vis.hosts[h] = struct{}{}
	}
}

type Visitor struct {
	db      *sql.DB
	mu      sync.Mutex
	hosts   map[string]struct{}
	Visited int64
}

func (v *Visitor) Filter(p *web.PageRequest, u *url.URL) error {
	v.mu.Lock()
	_, ok := v.hosts[u.Host]
	v.mu.Unlock()
	if ok {
		return nil
	}
	return web.ErrSkipURL
}

func (v *Visitor) LinkFound(u *url.URL) {}

func (v *Visitor) Visit(ctx context.Context, page *web.Page) {
	select {
	case <-ctx.Done():
		return
	default:
	}
	logVisit(page) // only handles logging
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	v.record(ctx, page)
	atomic.AddInt64(&v.Visited, 1)
}

func logVisit(page *web.Page) {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	l := log.WithFields(logrus.Fields{
		"depth":    page.Depth,
		"status":   page.Status,
		"hostname": hostname,
		"resp":     fmt.Sprintf("%v", page.ResponseTime),
	}).WithFields(memoryLogs(&mem))
	var info = l.Debugf
	// if page.Status >= 300 {
	// 	info = l.Warnf
	// }
	info("page{%s}", page.URL)
}

const insertPageSQL = `
INSERT INTO page (
	id,
	url,
	links,
	content_type,
	crawled_at,
	depth,
	redirected,
	redirected_from,
	status,
	response_time,
	ipv4,
	ipv6,
	title,
	keywords,
	chr_encoding
)
VALUES (
	$1, $2, $3, $4, $5, $6, $7, $8,
	$9, $10, $11, $12, $13, to_tsvector($14),
	$15
)
ON CONFLICT (id)
	DO UPDATE SET
		url             = $2,
		links           = $3,
		content_type    = $4,
		crawled_at      = $5,
		depth           = $6,
		redirected      = $7,
		redirected_from = $8,
		status          = $9,
		response_time   = $10,
		ipv4            = $11,
		ipv6            = $12,
		title           = $13,
		keywords        = to_tsvector($13) || to_tsvector($14),
		chr_encoding    = $15`

func (v *Visitor) record(ctx context.Context, page *web.Page) {
	var pageurl = page.URL.String()
	tx, err := v.db.BeginTx(ctx, nil)
	if err != nil {
		log.WithError(err).Error("could not create transaction")
		return
	}
	defer func() {
		switch err {
		case sql.ErrTxDone, sqldriver.ErrBadConn:
			return
		case nil, context.Canceled:
			err = tx.Commit()
			if err != nil && err != sql.ErrTxDone && err != context.Canceled {
				log.WithError(err).Error("could not commit transaction")
			}
		default:
			log.WithError(err).Error("rolling back database transaction")
			err = tx.Rollback()
			if err != nil && err != sql.ErrTxDone {
				log.WithError(err).Error("could not rollback transaction")
			}
			return
		}
	}()

	h := fnv.New128()
	io.WriteString(h, pageurl)
	id := h.Sum(nil)

	var p, e time.Duration

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		a := time.Now()
		defer wg.Done()
		err = insertPage(ctx, tx, id, page)
		if err != nil {
			return
		}
		p = time.Since(a)
	}()
	go func() {
		a := time.Now()
		defer wg.Done()
		err = insertEdges(ctx, tx, h, id, page)
		if err != nil {
			return
		}
		e = time.Since(a)
	}()
	wg.Wait()
	if Timed {
		log.WithFields(logrus.Fields{
			"page":  p,
			"edges": e,
			"links": len(page.Links),
		}).Info("done with visit")
	}
}

func insertPage(ctx context.Context, tx *sql.Tx, id []byte, page *web.Page) error {
	select {
	case <-ctx.Done():
		return nil
	default:
	}

	var (
		redirectedFrom string
		pageurl        = page.URL.String()
	)
	if page.Redirected {
		redirectedFrom = page.RedirectedFrom.String()
	}
	ipv4, ipv6, err := internal.IPAddrs(page.URL.Host)
	if err != nil {
		log.Warn("could not resolve ip for ", page.URL.Host)
		err = nil
	}

	_, err = tx.ExecContext(ctx, insertPageSQL,
		id,
		pageurl,
		pq.Array(urlStrings(page.Links)),
		page.ContentType,
		time.Now(),
		page.Depth,
		page.Redirected,
		redirectedFrom,
		page.Status,
		page.ResponseTime.String(),
		ipv4,
		ipv6,
		page.Title,
		strings.ToValidUTF8(strings.Join(page.Words, " "), ""),
		page.Encoding,
	)
	switch err {
	case nil, sql.ErrTxDone, context.Canceled, sqldriver.ErrBadConn:
		break
	default:
		log.WithFields(logrus.Fields{
			"error": err, "status": page.Status,
			"url":             pageurl,
			"redirected_from": page.RedirectedFrom,
			"content-type":    page.ContentType,
			"id":              hex.EncodeToString(id),
		}).Error("could not insert new page")
		return err
	}
	return nil
}

func insertEdges(ctx context.Context, tx *sql.Tx, h hash.Hash, id []byte, page *web.Page) error {
	select {
	case <-ctx.Done():
		return nil
	default:
	}
	pageurl := page.URL.String()

	_, err := tx.ExecContext(ctx, "DELETE FROM edge WHERE parent_id = $1", id)
	switch err {
	case nil, sql.ErrTxDone, sqldriver.ErrBadConn:
		break
	default:
		log.WithFields(logrus.Fields{
			"error": err, "url": pageurl,
		}).Error("could not delete existing edges")
		return err
	}
	if len(page.Links) == 0 {
		return nil
	}

	links := filterOutURL(pageurl, page.Links)
	if len(links) > 0 {
		edgeBuf, edgeValues := insertEdgesQuery(id, pageurl, links)
		query := edgeBuf.String()
		// Exec is the bottleneck here!
		_, err = tx.ExecContext(ctx, query, edgeValues...)
		switch err {
		case nil, context.Canceled, sqldriver.ErrBadConn:
			break
		default:
			log.WithError(err).Error("could not insert edges")
			return err
		}
	}
	return nil
}

func insertEdgesQuery(id []byte, pageurl string, links []string) (*bytes.Buffer, []interface{}) {
	var (
		n      = 1
		buf    bytes.Buffer
		values = make([]interface{}, 0, len(links)*3)
		nlinks = len(links) - 1
		h      = fnv.New128()
	)
	io.WriteString(&buf, "INSERT INTO edge (parent_id,child_id,child) VALUES ")
	for i, l := range links {
		h.Reset()
		io.WriteString(h, l)
		values = append(values, id, h.Sum(nil), l)
		io.WriteString(&buf, fmt.Sprintf("($%d,$%d,$%d)", n, n+1, n+2))
		n += 3
		if i < nlinks {
			buf.Write([]byte{','})
		}
	}
	io.WriteString(&buf, ` ON CONFLICT (parent_id,child_id,child) DO NOTHING`)
	return &buf, values
}

func filterOutURL(s string, from []*url.URL) []string {
	links := make([]string, 0, len(from))
	for _, l := range from {
		url := l.String()
		if url == s {
			continue
		}
		links = append(links, url)
	}
	return links
}

func urlStrings(urls []*url.URL) []string {
	s := make([]string, len(urls))
	for i, u := range urls {
		s[i] = u.String()
	}
	return s
}

func toMB(bytes uint64) float64 {
	return float64(bytes) / 1024.0 / 1024.0
}

func memoryLogs(mem *runtime.MemStats) logrus.Fields {
	lastGC := time.Since(time.Unix(0, int64(mem.LastGC)))
	return logrus.Fields{
		"heap":   fmt.Sprintf("%03.02fmb", toMB(mem.HeapAlloc)),
		"sys":    fmt.Sprintf("%03.02fmb", toMB(mem.Sys)),
		"frees":  mem.Frees,
		"GCs":    mem.NumGC,
		"lastGC": lastGC.Truncate(time.Millisecond),
	}
}
