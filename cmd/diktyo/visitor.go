package main

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/harrybrwn/diktyo/web"
	"github.com/lib/pq"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
)

type visitor struct {
	db    *sql.DB
	mu    sync.Mutex
	hosts map[string]struct{}
}

func (v *visitor) String() string {
	return fmt.Sprintf("visitor{hosts: %#v, db: %v}", v.hosts, v.db)
}

func (v *visitor) Visit(page *web.Page) {
	logVisit(page) // only handles logging
	// ctx, cancel := context.WithCancel(context.Background())
	// defer cancel()
	// v.record(ctx, page)
}

const insertEdgeSQL = `
INSERT INTO edge (
	parent, child
) VALUES (
	$1, $2
)
ON CONFLICT (parent, child)
	DO NOTHING`

const insertPageSQL = `
INSERT INTO page (
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
	keywords
)
VALUES (
	$1, $2, $3, $4, $5, $6, $7, $8,
	$9, $10, $11, to_tsvector($12)
)
ON CONFLICT (url)
	DO UPDATE SET
		links           = $2,
		content_type    = $3,
		crawled_at      = $4,
		depth           = $5,
		redirected      = $6,
		redirected_from = $7,
		status          = $8,
		response_time   = $9,
		ipv4            = $10,
		ipv6            = $11,
		keywords        = to_tsvector($12)`

func logVisit(page *web.Page) {
	mem := getMemStats()
	l := log.WithFields(logrus.Fields{
		"depth":  page.Depth,
		"status": page.Status,
	}).WithFields(memoryLogs(mem))
	var info = l.Infof
	if page.Status >= 300 {
		info = l.Warnf
	}
	info("page{%d, %v, %s}", page.Depth, page.ResponseTime, page.URL)
}

func (v *visitor) record(ctx context.Context, page *web.Page) {
	var redirectedFrom string
	if page.Redirected {
		redirectedFrom = page.RedirectedFrom.String()
	}
	ipv4, ipv6, err := ipAddrs(page.URL.Host)
	if err != nil {
		log.Warn("could not resolve ip for ", page.URL.Host)
		err = nil
	}

	var keywords [][]byte
	switch page.ContentType {
	case "image/png", "image/jpeg", "image/gif":
	default:
		keywords, err = page.Keywords()
		if err != nil {
			log.Error(errors.Wrap(err, "could not get page keywords"))
			err = nil
		}
	}

	pageurl := page.URL.String()
	tx, err := v.db.Begin()
	if err != nil {
		log.Error(err)
		return
	}
	defer func() {
		if err != nil {
			log.WithError(err).Error("rolling back database transaction")
			tx.Rollback()
		} else {
			tx.Commit()
		}
	}()

	_, err = tx.Exec(insertPageSQL,
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
		bytes.Join(keywords, []byte{' '}),
	)
	if err != nil {
		log.WithFields(logrus.Fields{
			"error":           err,
			"status":          page.Status,
			"url":             pageurl,
			"redirected_from": page.RedirectedFrom,
			"links":           page.Links,
			"depth":           page.Depth,
			"content-type":    page.ContentType,
		}).Error("could not insert new page")
		return
	}

	if len(page.Links) == 0 {
		return
	}

	stmt, err := tx.PrepareContext(ctx, insertEdgeSQL)
	// stmt, err := tx.PrepareContext(ctx, "INSERT INTO edge (parent, child) VALUES ($1, $2)")
	// stmt, err := tx.PrepareContext(ctx, pq.CopyIn("edge", "parent", "child"))
	if err != nil {
		return
	}
	defer stmt.Close()
	for _, l := range page.Links {
		childurl := l.String()
		if childurl == pageurl {
			continue
		}
		_, err = stmt.Exec(pageurl, childurl)
		if err != nil {
			log.WithError(err).WithFields(logrus.Fields{
				"parent": pageurl,
				"child":  childurl,
			}).Error(err)
			return
		}
	}

}

func urlStrings(urls []*url.URL) []string {
	s := make([]string, len(urls))
	for i, u := range urls {
		s[i] = u.String()
	}
	return s
}

func (v *visitor) Filter(p *web.PageRequest) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	_, ok := v.hosts[p.URL.Host]
	if ok {
		return nil
	}
	return web.ErrSkipURL
}

func (v *visitor) LinkFound(u *url.URL) {}

func (v *visitor) addHosts(hosts []string) {
	for _, h := range hosts {
		v.hosts[h] = struct{}{}
	}
}
