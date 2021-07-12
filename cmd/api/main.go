package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/harrybrwn/config"
	"github.com/harrybrwn/diktyo/cmd"
	"github.com/harrybrwn/diktyo/db"
	_ "github.com/lib/pq"
)

func main() {
	var (
		conf cmd.Config
	)
	config.SetConfig(&conf)
	config.AddFile("config.yaml")
	config.SetType("yaml")
	config.AddPath("/var/local/remora")
	config.AddPath(".")
	config.ReadConfigFile()

	db, err := db.New(&conf.DB)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	http.HandleFunc("/search", func(rw http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		query := q.Get("q")
		if query == "" {
			fmt.Fprintf(rw, `{"error":"received empty query"}`)
			rw.WriteHeader(400)
			return
		}
		limit, err := intQuery(q, "limit")
		if err != nil {
			fmt.Fprintf(rw, `{"error":"bad limit url query"}`)
			rw.WriteHeader(400)
			return
		}
		pages, err := SearchPages(ctx, db, query, limit)
		if err != nil {
			rw.WriteHeader(500)
			return
		}
		b, err := json.Marshal(pages)
		if err != nil {
			rw.WriteHeader(500)
			return
		}
		_, err = rw.Write(b)
		if err != nil {
			rw.WriteHeader(500)
			return
		}
		rw.WriteHeader(http.StatusOK)
	})

	http.HandleFunc("/", func(rw http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(rw, `{"error":"this api is not implemented yet"}`)
		rw.WriteHeader(http.StatusNotImplemented)
	})

	err = http.ListenAndServe(":8080", nil)
	if err != nil {
		log.Fatal(err)
	}
}

func SearchPages(
	ctx context.Context,
	db *sql.DB,
	query string,
	limit int,
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
			ts_rank(keywords, query) as rank
		FROM page, websearch_to_tsquery($1) query
		WHERE
			keywords @@ query
		ORDER BY rank DESC LIMIT $2`, query, limit)
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
			&p.QueryRank,
		)
		if err != nil {
			return nil, err
		}
	}
	return pages, nil
}

type Page struct {
	ID        []byte    `json:"id"`
	URL       *url.URL  `json:"url"`
	IPv4      net.IP    `json:"ipv4"`
	IPv6      net.IP    `json:"ipv6"`
	CrawledAt time.Time `json:"crawled_at"`
	QueryRank float32   `json:"rank"`
}

type URLQuery interface {
	Get(string) string
}

func intQuery(queries URLQuery, key string) (int, error) {
	q := queries.Get(key)
	i, err := strconv.ParseInt(q, 10, 32)
	return int(i), err
}
