package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/harrybrwn/config"
	"github.com/harrybrwn/diktyo/cmd"
	"github.com/harrybrwn/diktyo/db"
	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
	"github.com/sirupsen/logrus"
)

var log = logrus.StandardLogger()

func main() {
	var conf cmd.Config
	godotenv.Load()
	config.SetConfig(&conf)
	config.AddFile("config.yml")
	config.SetType("yaml")
	config.AddPath("/var/local/remora")
	config.AddPath(".")
	config.InitDefaults()
	err := config.ReadConfigFile()
	if err != nil {
		log.Fatal(err)
	}

	db, err := db.New(&conf.DB)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	http.HandleFunc("/", func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(http.StatusNotImplemented)
		fmt.Fprintf(rw, `{"error":"this api is not implemented yet"}`)
	})

	http.HandleFunc("/search", searchHandler(ctx, db))
	err = http.ListenAndServe(":8080", nil)
	if err != nil {
		log.Fatal(err)
	}
}

func searchHandler(ctx context.Context, db *sql.DB) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		start := time.Now()
		q := r.URL.Query()
		query := q.Get("q")
		if query == "" {
			rw.WriteHeader(400)
			fmt.Fprintf(rw, `{"error":"received empty query"}`)
			return
		}
		limit, err := intQuery(q, "limit")
		if err != nil {
			rw.WriteHeader(400)
			fmt.Fprintf(rw, `{"error":"bad limit url query"}`)
			return
		}
		log.WithFields(logrus.Fields{
			"query": query,
			"limit": limit,
			"from":  r.RemoteAddr,
		}).Info("handling search query")

		rw.Header().Set("Content-Type", "application/json")
		pages, err := SearchPages(ctx, db, query, limit)
		if err != nil {
			rw.WriteHeader(500)
			writeErr(err, rw)
			return
		}
		b, err := json.Marshal(pages)
		if err != nil {
			rw.WriteHeader(500)
			writeErr(err, rw)
			return
		}
		_, err = rw.Write(b)
		if err != nil {
			rw.WriteHeader(500)
			writeErr(err, rw)
			return
		}
		defer log.WithField(
			"time", time.Since(start),
		).Info("search results returned")
		rw.WriteHeader(http.StatusOK)
	}
}

func writeErr(err error, w io.Writer) {
	fmt.Fprintf(w, `{"error":%q}`, err.Error())
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
			&p.QueryRank,
		)
		pages = append(pages, &p)
		if err != nil {
			return nil, err
		}
	}
	return pages, nil
}

type Page struct {
	ID        []byte    `json:"id"`
	URL       string    `json:"url"`
	IPv4      IP        `json:"ipv4"`
	IPv6      IP        `json:"ipv6"`
	CrawledAt time.Time `json:"crawled_at"`
	QueryRank float32   `json:"rank"`
}

type IP net.IP

func (ip *IP) Scan(src interface{}) error {
	switch v := src.(type) {
	case []uint8:
		i := net.ParseIP(string(v))
		*ip = IP(i)
	case string:
		i := net.ParseIP(v)
		*ip = IP(i)
	default:
		return fmt.Errorf("unknown type \"%T\"", src)
	}
	return nil
}

func (ip *IP) MarshalJSON() ([]byte, error) {
	res := []byte{'"'}
	res = append(res, []byte(net.IP(*ip).String())...)
	res = append(res, '"')
	return res, nil
}

func (ip *IP) UnmarshalJSON(b []byte) error {
	*ip = IP(net.ParseIP(string(b)))
	return nil
}

var (
	_ json.Marshaler   = (*IP)(nil)
	_ json.Unmarshaler = (*IP)(nil)
)

type URLQuery interface {
	Get(string) string
}

func intQuery(queries URLQuery, key string) (int, error) {
	q := queries.Get(key)
	i, err := strconv.ParseInt(q, 10, 32)
	return int(i), err
}
