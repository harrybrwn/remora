package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Page struct {
	ID           []byte    `json:"id"`
	URL          string    `json:"url"`
	IPv4         IP        `json:"ipv4"`
	IPv6         IP        `json:"ipv6"`
	CrawledAt    time.Time `json:"crawled_at"`
	Title        string    `json:"title,omitempty"`
	Encoding     string    `json:"encoding,omitempty"`
	ResponseTime string    `json:"response_time"`
	QueryRank    float32   `json:"rank,omitempty"`
}

type Database interface {
	QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error)
}

func SearchPages(
	ctx context.Context,
	db Database,
	query string,
	limit int64,
) ([]*Page, error) {
	var (
		pages = make([]*Page, 0)
	)
	rows, err := db.QueryContext(
		ctx,
		`SELECT
			id,
			url,
			ipv4,
			ipv6,
			crawled_at,
			title,
			chr_encoding,
			ts_rank(keywords, query) as rank
		FROM page, websearch_to_tsquery($1) query
		WHERE
			keywords @@ query
		ORDER BY rank DESC LIMIT $2`,
		query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p Page
		err = rows.Scan(
			&p.ID,
			&p.URL,
			&p.IPv4,
			&p.IPv6,
			&p.CrawledAt,
			&p.Title,
			&p.Encoding,
			&p.QueryRank,
		)
		if err != nil {
			return nil, err
		}
		p.Encoding = strings.ToLower(p.Encoding)
		pages = append(pages, &p)
	}
	return pages, nil
}

func CreateListParams(q url.Values) (*ListParams, error) {
	var (
		err    error
		order  = q.Get("order_by")
		params = ListParams{
			Host:    q.Get("host"),
			Reverse: len(q.Get("rev")) > 0,
			OrderBy: "",
			Limit:   0,
		}
		lim = q.Get("limit")
	)
	if lim == "" {
		return &params, nil
	}
	switch order {
	case "":
		break
	case
		"response_time",
		"crawled_at",
		"status",
		"title",
		"depth",
		"id",
		"ipv4",
		"ipv6",
		"url":
		params.OrderBy = order
	default:
		return &params, &StatusError{
			Err:    fmt.Errorf("unknown order parameter %q", order),
			Status: http.StatusBadRequest,
			Reason: "failed to parse url queries",
		}
	}
	params.Limit, err = strconv.ParseInt(lim, 10, 64)
	if err != nil {
		return &params, wrapstatus(err, 400, "invalid limit query")
	}
	return &params, nil
}

type ListParams struct {
	Host    string
	OrderBy string
	Limit   int64
	Reverse bool
}

func ListPages(ctx context.Context, db Database, params *ListParams) ([]*Page, error) {
	var (
		b     bytes.Buffer
		pages = make([]*Page, 0, params.Limit)
	)
	_, err := b.WriteString("SELECT id, url, ipv4, ipv6, crawled_at, title, chr_encoding, response_time FROM page")
	if err != nil {
		return nil, err
	}
	args, err := buildListQueryParameters(&b, params)
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var p Page
		err = rows.Scan(
			&p.ID,
			&p.URL,
			&p.IPv4, &p.IPv6,
			&p.CrawledAt,
			&p.Title,
			&p.Encoding,
			&p.ResponseTime,
		)
		if err != nil {
			return nil, err
		}
		p.Encoding = strings.ToLower(p.Encoding)
		pages = append(pages, &p)
	}
	return pages, nil
}

func buildListQueryParameters(w io.Writer, p *ListParams) (res []interface{}, err error) {
	if p.Host != "" {
		fmt.Fprintf(w, " WHERE url LIKE '%%' || $%d || '%%'", len(res)+1)
		if err != nil {
			return
		}
		res = append(res, p.Host)
	}
	if p.OrderBy != "" {
		fmt.Fprintf(w, " ORDER BY $%d", len(res)+1)
		res = append(res, p.OrderBy)
		if p.Reverse {
			w.Write([]byte(" DESC"))
		}
	}
	if p.Limit != 0 {
		fmt.Fprintf(w, " LIMIT $%d", len(res)+1)
		res = append(res, p.Limit)
	}
	return
}

type IP net.IP

func (ip *IP) Scan(src interface{}) error {
	if src == nil {
		return nil
	}
	switch v := src.(type) {
	case []uint8:
		*ip = IP(net.ParseIP(string(v)))
	case string:
		*ip = IP(net.ParseIP(v))
	default:
		return fmt.Errorf("unknown type \"%T\"", src)
	}
	return nil
}

func (ip *IP) MarshalJSON() ([]byte, error) {
	if len(*ip) == 0 {
		return []byte{'"', '"'}, nil
	}
	res := []byte{'"'}
	res = append(res, []byte(net.IP(*ip).String())...)
	res = append(res, '"')
	return res, nil
}

func (ip *IP) UnmarshalJSON(b []byte) error {
	*ip = IP(net.ParseIP(string(b)))
	return nil
}

type Duration time.Duration

func (d *Duration) Scan(src interface{}) error {
	if src == nil {
		*d = 0
		return nil
	}
	switch v := src.(type) {
	case []uint8:
		dur, err := time.ParseDuration(string(v))
		if err != nil {
			return err
		}
		*d = Duration(dur)
	default:
		return fmt.Errorf("cannot parse type %T into Duration", src)
	}
	return nil
}

func (d Duration) String() string {
	return time.Duration(d).String()
}

var (
	_ json.Marshaler   = (*IP)(nil)
	_ json.Unmarshaler = (*IP)(nil)
)
