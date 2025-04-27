package visitor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	sqldriver "database/sql/driver"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/harrybrwn/remora/db"
	"github.com/harrybrwn/remora/internal"
	"github.com/harrybrwn/remora/internal/region"
	"github.com/harrybrwn/remora/storage"
	"github.com/harrybrwn/remora/web"
	"github.com/lib/pq"
	"github.com/sirupsen/logrus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

const Timed = true

var (
	log      *logrus.Logger = logrus.New()
	hostname string

	Verbose = false
)

func init() {
	var err error
	hostname, err = os.Hostname()
	if err != nil {
		log.Warning("could not get hostname")
	}
}

func SetLogger(l *logrus.Logger) { log = l }

func New(sqlDB *sql.DB, redis storage.Redis) (*Visitor, error) {
	return &Visitor{
		db: db.Wrap(sqlDB, db.WithName("visitor-db")),
		hashes: hashSet{
			r:      redis,
			region: region.NewRegion(otel.Tracer("remora/visitor.redisHashset"))},
		Hosts: make(map[string]struct{}),
		// hash:  func() hash.Hash { return fnv.New128() },
		hash: func() hash.Hash { return sha256.New() },
	}, nil
}

func AddHost(v web.Visitor, host ...string) {
	AddHosts(v, host)
}

func AddHosts(v web.Visitor, hosts []string) {
	switch vis := v.(type) {
	case *Visitor:
		for _, h := range hosts {
			vis.Hosts[h] = struct{}{}
		}
	case *FSVisitor:
		for _, h := range hosts {
			vis.Hosts[h] = struct{}{}
		}
	default:
		panic("cannot add hosts to this visitor")
	}
}

type HashBuilder func() hash.Hash

type Visitor struct {
	db      db.DB
	Hosts   map[string]struct{}
	hashes  hashSet
	Visited int64
	hash    HashBuilder
}

func (v *Visitor) Close() error { return nil }

func (v *Visitor) Filter(p *web.PageRequest, u *url.URL) error {
	return nil
}

func (v *Visitor) LinkFound(u *url.URL) error { return nil }

func (v *Visitor) Visit(ctx context.Context, page *web.Page) {
	select {
	case <-ctx.Done():
		return
	default:
	}
	if v.hashes.has(ctx, page.Hash) {
		log.WithFields(logrus.Fields{
			"url":  page.URL.String(),
			"hash": hex.EncodeToString(page.Hash[:]),
		}).Warn("already seen page content hash")
		return
	}
	v.hashes.put(ctx, page.Hash)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if page.Status != 200 {
		logVisit(page, v.Visited) // only handles logging
	} else if Verbose {
		logVisit(page, v.Visited)
	}
	v.record(ctx, page)
	atomic.AddInt64(&v.Visited, 1)
}

func logVisit(page *web.Page, n int64) {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)
	l := log.WithFields(logrus.Fields{
		"n":        n,
		"depth":    page.Depth,
		"status":   fmt.Sprintf("%d %s", page.Status, http.StatusText(page.Status)),
		"hostname": hostname,
		"resp":     fmt.Sprintf("%v", page.ResponseTime),
	}).WithFields(memoryLogs(&mem))
	var info = l.Debugf
	if page.Status >= 300 {
		info = l.Warnf
	}
	info("page{%s}", page.URL)
}

var insertPageSQL = `
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

func init() {
	// Programatically making the SQL query smaller
	insertPageSQL = strings.ReplaceAll(insertPageSQL, "\t", " ")
	insertPageSQL = strings.ReplaceAll(insertPageSQL, "\n", " ")
	pat, err := regexp.Compile("[ ]+")
	if err != nil {
		panic(err)
	}
	insertPageSQL = pat.ReplaceAllString(insertPageSQL, " ")
	insertPageSQL = strings.Replace(insertPageSQL, " = ", "=", -1)
	insertPageSQL = strings.Replace(insertPageSQL, "( ", "(", -1)
	insertPageSQL = strings.Replace(insertPageSQL, " )", ")", -1)
	insertPageSQL = strings.Replace(insertPageSQL, ", ", ",", -1)
}

func (v *Visitor) record(ctx context.Context, page *web.Page) {
	var pageurl = page.URL.String()
	tx, err := v.db.BeginTx(ctx,
		db.TxReadOnly(false),
		db.TxIsolation(sql.LevelRepeatableRead),
	)
	// tx, err := v.db.BeginTx(ctx, &sql.TxOptions{
	// 	ReadOnly:  false,
	// 	Isolation: sql.LevelRepeatableRead,
	// })
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

	h := v.hash()
	io.WriteString(h, pageurl)
	id := h.Sum(nil)

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		err = insertPage(ctx, tx, id, page)
		if err != nil {
			log.WithError(err).Error("could not insert page")
			return
		}
	}()
	go func() {
		defer wg.Done()
		err = insertEdges(ctx, tx, h, id, page)
		if err != nil {
			log.WithError(err).Error("could not insert edges")
			return
		}
	}()
	wg.Wait()
}

type execerCtx interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func insertPage(ctx context.Context, handle execerCtx, id []byte, page *web.Page) error {
	select {
	case <-ctx.Done():
		return nil
	default:
	}

	var (
		redirectedFrom string
		pageurl        = page.URL.String()
		status         int
	)
	if page.Redirected {
		redirectedFrom = page.RedirectedFrom.String()
	}
	ipv4, ipv6, err := internal.IPAddrs(page.URL.Host)
	if err != nil {
		log.Warn("could not resolve ip for ", page.URL.Host)
		err = nil
	}
	status = page.Response.StatusCode

	_, err = handle.ExecContext(ctx, insertPageSQL,
		id,
		pageurl,
		pq.Array(urlStrings(page.Links)),
		page.ContentType,
		time.Now(),
		page.Depth,
		page.Redirected,
		redirectedFrom,
		status,
		page.ResponseTime.String(),
		ipv4,
		ipv6,
		page.Title(),
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

func insertEdges(ctx context.Context, tx db.Tx, h hash.Hash, id []byte, page *web.Page) error {
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
			log.WithFields(logrus.Fields{
				"error": err,
				"url":   pageurl,
			}).Error("could not insert edges")
			return err
		}
	}
	return nil
}

func insertEdgesQuery(id []byte, pageurl string, links []string) (*bytes.Buffer, []interface{}) {
	var (
		n      = 1
		buf    bytes.Buffer
		values = make([]any, 0, len(links)*3)
		nlinks = len(links) - 1
		// h      = fnv.New128()
		h = sha256.New()
	)
	io.WriteString(&buf, "INSERT INTO edge(parent_id,child_id,child)VALUES ")
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
	io.WriteString(&buf, ` ON CONFLICT(parent_id,child_id)DO NOTHING`)
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

type hashSet struct {
	r      storage.Redis
	region *region.Region
}

func (h *hashSet) has(ctx context.Context, b [16]byte) bool {
	var (
		has bool
		err error
	)
	h.region.Wrap(ctx, "HashSet.has", func(ctx context.Context) ([]attribute.KeyValue, error) {
		err = h.r.Get(ctx, hex.EncodeToString(b[:])).Err()
		has = err != redis.Nil
		if err == redis.Nil {
			err = nil
		}
		return []attribute.KeyValue{
			{Key: "has.return", Value: attribute.BoolValue(has)},
		}, err
	})
	return has
}

func (h *hashSet) put(ctx context.Context, b [16]byte) error {
	var err error
	h.region.Wrap(ctx, "HashSet.put", func(ctx context.Context) ([]attribute.KeyValue, error) {
		err = h.r.Set(ctx, hex.EncodeToString(b[:]), 2, 0).Err()
		return nil, err
	})
	return err
}
