package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"time"

	"github.com/harrybrwn/config"
	"github.com/harrybrwn/remora/cmd"
	"github.com/harrybrwn/remora/db"
	"github.com/harrybrwn/remora/internal/logging"
	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
	"github.com/sirupsen/logrus"
)

var log = logrus.StandardLogger()

func main() {
	var (
		conf  cmd.Config
		start = time.Now()
		port  = 8080
	)
	log.SetLevel(logrus.DebugLevel)
	godotenv.Load()
	config.SetConfig(&conf)
	config.AddFile("config.yml")
	config.SetType("yaml")
	config.AddPath("/var/local/remora")
	config.AddPath(".")
	config.InitDefaults()
	err := config.ReadConfig()
	if err != nil {
		log.Fatal(err)
	}

	flag.IntVar(&port, "p", port, "run server with a different port")
	flag.Parse()

	db, err := db.New(&conf.DB)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	var (
		ctx = context.Background()
		mux = http.NewServeMux()
		srv = http.Server{
			Addr:    fmt.Sprintf(":%d", port),
			Handler: logging.LogHTTPRequests(log)(mux),
		}
	)

	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()
	mux.HandleFunc("/", func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		rw.WriteHeader(http.StatusNotImplemented)
		fmt.Fprintf(rw, `{"error":"this api is not implemented yet"}`)
	})
	mux.HandleFunc("/list", listHandler(ctx, db))
	mux.HandleFunc("/search", searchHandler(ctx, db))
	mux.HandleFunc("/rank", pageRankHandler(ctx, db))

	go func() {
		<-ctx.Done()
		err = srv.Shutdown(ctx)
		if err != nil {
			log.Error(err)
		}
	}()
	logstartup(start, &conf)
	switch srv.ListenAndServe() {
	case nil, http.ErrServerClosed:
		return
	default:
		cancel()
		log.Fatal(err)
	}
}

func init() {
	f := logging.PrefixedFormatter{
		Prefix:     "api",
		TimeFormat: time.Stamp,
	}
	log.SetFormatter(&f)
	log.SetOutput(os.Stdout)
}

type response struct {
	http.ResponseWriter
	status int
	err    error
}

func (r *response) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func logall(h http.Handler) http.Handler {
	return http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		var (
			resp = response{
				ResponseWriter: rw,
				status:         200,
				err:            nil,
			}
			start = time.Now()
		)
		h.ServeHTTP(&resp, r)
		fields := logrus.Fields{
			"latency": time.Since(start),
			"status":  resp.status,
		}
		if resp.err != nil {
			fields["error"] = resp.err
		}
		l := log.WithFields(fields)
		var lg func(string, ...interface{})
		if resp.status < 300 {
			lg = l.Infof
		} else if resp.status < 400 {
			lg = l.Infof
		} else if resp.status < 500 {
			lg = l.Warnf
		} else if resp.status >= 500 {
			lg = l.Errorf
		}
		lg("%d %s %s", resp.status, r.Method, r.URL)
	})
}

func listHandler(
	ctx context.Context,
	db *sql.DB,
) func(rw http.ResponseWriter, r *http.Request) {
	return func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set("Content-Type", "application/json")
		if r.Method != "GET" {
			fail(rw)
			return
		}
		var (
			err   error
			pages []*Page
		)
		params, err := CreateListParams(r.URL.Query())
		if err != nil {
			fail(rw, err)
			return
		}
		pages, err = ListPages(ctx, db, params)
		if err != nil {
			fail(rw, internal(err))
			return
		}
		b, err := json.Marshal(pages)
		if err != nil {
			fail(rw, internal(err))
			return
		}
		if _, err = rw.Write(b); err != nil {
			fail(rw, internal(err))
			return
		}
	}
}

func searchHandler(ctx context.Context, db *sql.DB) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			fail(rw)
			return
		}
		q := r.URL.Query()
		query := q.Get("q")
		if query == "" {
			fail(rw, wrapstatus(ErrBadURLQuery, 400, "received empty query"))
			return
		}
		limit, err := intQuery(q, "limit")
		if err != nil {
			fail(rw, wrapstatus(ErrBadURLQuery, 400, "bad limit url query"))
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
			fail(rw, internal(err))
			return
		}
		b, err := json.Marshal(pages)
		if err != nil {
			fail(rw, internal(err))
			return
		}
		_, err = rw.Write(b)
		if err != nil {
			fail(rw, internal(err))
			return
		}
	}
}

func childrenHandler(ctx context.Context, db *sql.DB) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		u := r.URL.Query().Get("url")
		if u == "" {
			fail(rw, wrapstatus(ErrBadURLQuery, 400, "no url given in query"))
			return
		}
		rows, err := db.QueryContext(
			ctx,
			`SELECT
				id,url,ipv4,ipv6,crawled_at,response_time,count(child_id)
			FROM page, edge
			WHERE
				edge.parent_id = page.id AND
				page.url = $1`, u)
		if err != nil {
			fail(rw, internal(err))
			return
		}
		defer rows.Close()
		var pages = make([]Page, 0)
		for rows.Next() {
			var p Page
			err := rows.Scan(
				&p.ID,
				&p.URL,
				&p.IPv4,
				&p.IPv6,
				&p.CrawledAt,
				&p.ResponseTime,
				&p.QueryRank,
			)
			if err != nil {
				fail(rw, internal(err))
				return
			}
			pages = append(pages, p)
		}
		b, err := json.Marshal(pages)
		if err != nil {
			fail(rw, internal(err))
			return
		}
		if _, err = rw.Write(b); err != nil {
			fail(rw, internal(err))
			return
		}
	}
}

func pageRankHandler(ctx context.Context, db *sql.DB) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		u := r.URL.Query().Get("url")
		if u == "" {
			fail(rw, wrapstatus(ErrBadURLQuery, 400, "no url given in query"))
			return
		}
		row := db.QueryRowContext(
			ctx,
			`SELECT
				id,url,ipv4,ipv6,crawled_at,response_time
			FROM page, edge
			WHERE
				edge.child_id = page.id AND
				page.url = $1`, u)
		var p Page
		err := row.Scan(
			&p.ID,
			&p.URL,
			&p.IPv4,
			&p.IPv6,
			&p.CrawledAt,
			&p.ResponseTime,
			&p.QueryRank,
		)
		if err != nil {
			fail(rw, internal(err))
			return
		}
		b, err := json.Marshal(&p)
		if err != nil {
			fail(rw, internal(err))
			return
		}
		if _, err = rw.Write(b); err != nil {
			fail(rw, internal(err))
			return
		}
	}
}

type URLQuery interface {
	Get(string) string
}

func intQuery(queries URLQuery, key string) (int64, error) {
	q := queries.Get(key)
	i, err := strconv.ParseInt(q, 10, 32)
	return i, err
}

func logstartup(t time.Time, conf *cmd.Config) {
	log.WithFields(logrus.Fields{
		"startup": time.Since(t),
		"db":      fmt.Sprintf("%s:%d/%s", conf.DB.Host, conf.DB.Port, conf.DB.Name),
	}).Info("Starting server")
}
