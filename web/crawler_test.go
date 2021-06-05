package web

import (
	"context"
	_ "embed"
	"fmt"
	"html/template"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/dgraph-io/badger/v3"
)

func Test(t *testing.T) {
	var (
		err error
	)
	u := urlsfunc(t)
	l := newLocalListener()
	base := "http://" + l.Addr().String()
	testpages := make([]*testpage, 0)
	for i := 0; i < 500; i++ {
		l := make([]string, 0)
		for j := 0; j < 200; j++ {
			l = append(l, "/"+strconv.FormatInt(int64(rand.Intn(500)), 10))
		}
		testpages = append(testpages, &testpage{
			Title: strconv.FormatInt(int64(i), 10),
			Links: u(base, l...),
		})
	}
	srv := newserver(l, newTestSite(testpages))
	srv.Start()
	defer srv.Close()
	HttpClient = srv.Client()

	c := NewCrawler(
		WithVisitor(&testvisitor{}),
		WithLimit(2),
		WithQueueSize(100),
	)

	opts := badger.DefaultOptions("")
	opts.InMemory = true
	c.DB, err = badger.Open(opts)
	if err != nil {
		t.Error(err)
	}
	page := NewPageFromString(base+"/1", 0)
	page.Fetch()
	fmt.Println(page)
	c.Enqueue(page)
	ctx, stop := context.WithCancel(context.Background())
	c.Crawl(ctx)
	defer stop()
	<-ctx.Done()
}

type testvisitor struct {
	NoOpVisitor
}

func (tv *testvisitor) LinkFound(u *url.URL) {
	fmt.Println(u)
}

func TestFetch(t *testing.T) {
	t.Run("Fetch en.wikipedia", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(testHTTPHandler))
		defer srv.Close()
		HttpClient = srv.Client()
		page := NewPageFromString(srv.URL+"/", 0)
		err := page.Fetch()
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Links) != 278 {
			t.Error("wrong number of links")
		}
		if page.ContentType != "text/html" {
			t.Error("wrong content type")
		}
	})

	t.Run("Fetch test pages", func(t *testing.T) {
		u := urlsfunc(t)
		l := newLocalListener()
		base := "http://" + l.Addr().String()
		testpages := []*testpage{
			{"one", u(base, "/one", "/two", "/three")},
			{"two", u(base, "/three", "/four", "/one/one")},
			{"three", u(base, "/one", "/two", "/four")},
			{"four", u(base, "/1/2/3")},
			{"one/one", u(base)},
		}
		srv := newserver(l, newTestSite(testpages))
		srv.Start()
		defer srv.Close()
		for i, path := range []string{
			"/one",
			"/two",
			"/three",
			"/four",
			"/one/one",
		} {
			page := NewPageFromString(srv.URL+path, 0)
			err := page.Fetch()
			if err != nil {
				t.Fatal(err)
			}
			sort.Sort(URLs(page.Links))
			sort.Sort(URLs(testpages[i].Links))
			if !urlsEq(page.Links, testpages[i].Links) {
				t.Errorf("url list %d is incorrect:\ngot  %v,\nwant %v", i+1, page.Links, testpages[i].Links)
			}
		}
	})
}

func urlsfunc(t *testing.T) func(string, ...string) []*url.URL {
	return func(base string, ss ...string) []*url.URL {
		t.Helper()
		var err error
		urls := make([]*url.URL, len(ss))
		for i, s := range ss {
			urls[i], err = url.Parse(base + s)
			if err != nil {
				t.Error(err)
			}
		}
		return urls
	}
}

//go:embed testdata/en.wikipedia.org.html
var testwiki []byte

func testHTTPHandler(w http.ResponseWriter, r *http.Request) {
	w.Write(testwiki)
}

func newTestSite(pages []*testpage) http.Handler {
	mux := http.NewServeMux()
	for _, page := range pages {
		mux.Handle("/"+page.Title, page)
	}
	return mux
}

type testpage struct {
	Title string
	Links []*url.URL
}

// ServeHTTP serves the test page to an http request
func (tp *testpage) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	genpage(w, tp)
}

func genpage(w io.Writer, tp *testpage) error {
	tmpl, err := template.New("testpage").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta http-equiv="X-UA-Compatible" content="IE=edge">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>{{.Title}}</title>
</head>
<body>
    <h1>This page is titled {{.Title}}</h1>
{{- range .Links }}
    <a href="{{.}}">{{.Path}}</a>
{{- end }}
</body>
</html>` + "\n")
	if err != nil {
		return err
	}
	return tmpl.Execute(w, tp)
}

func newLocalListener() net.Listener {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		if l, err = net.Listen("tcp6", "[::1]:0"); err != nil {
			panic(fmt.Sprintf("httptest: failed to listen on a port: %v", err))
		}
	}
	return l
}

func eq(a, b string) bool { return a == b }

func urlEq(a, b *url.URL) bool {
	return eq(a.Scheme, b.Scheme) &&
		eq(a.Host, b.Host) &&
		eq(a.Path, b.Path) &&
		eq(a.RawQuery, b.RawQuery)
}

func urlsEq(a, b []*url.URL) bool {
	if len(a) != len(b) {
		return false
	}
	l := len(a)
	for i := 0; i < l; i++ {
		if !urlEq(a[i], b[i]) {
			return false
		}
	}
	return true
}

type URLs []*url.URL

func (u URLs) Len() int           { return len(u) }
func (u URLs) Less(i, j int) bool { return strings.Compare(u[i].String(), u[j].String()) == -1 }
func (u URLs) Swap(i, j int)      { u[i], u[j] = u[j], u[i] }

func newserver(l net.Listener, h http.Handler) *httptest.Server {
	return &httptest.Server{
		Listener: l,
		Config:   &http.Server{Handler: h},
	}
}

func inMemDB(t *testing.T) *badger.DB {
	opts := badger.DefaultOptions("")
	opts.InMemory = true
	db, err := badger.Open(opts)
	if err != nil {
		t.Fatal(err)
	}
	return db
}
