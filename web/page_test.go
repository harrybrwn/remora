package web

import (
	"bytes"
	_ "embed"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/matryer/is"
)

func Test(t *testing.T) {
}

func TestParseCacheControl(t *testing.T) {
	is := is.New(t)
	for _, tst := range []struct {
		s string
		Control
	}{
		{
			"max-age=3600, s-max-age=600, public, must-revalidate",
			Control{
				Scope:          public,
				MaxAge:         time.Second * 3600,
				SharedMaxAge:   time.Second * 600,
				MustRevalidate: true,
			},
		},
	} {
		ctrl, err := parseCacheControl(tst.s)
		is.NoErr(err)
		is.Equal(ctrl.MaxAge, tst.MaxAge)
		is.Equal(ctrl.SharedMaxAge, tst.SharedMaxAge)
		is.Equal(ctrl.Scope, tst.Scope)
		is.Equal(ctrl.MustRevalidate, tst.MustRevalidate)
		is.Equal(ctrl.NoTransform, tst.NoTransform)
		is.Equal(ctrl.OnlyIfCached, tst.OnlyIfCached)
	}
}

func sitemapHandler(baseurl string) http.HandlerFunc {
	data := struct {
		URL string
	}{
		URL: baseurl,
	}
	handler := func(rw http.ResponseWriter, r *http.Request) {
		maindata := `
<?xml version='1.0' encoding='UTF-8'?>
	<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xsi:schemaLocation="http://www.sitemaps.org/schemas/sitemap/0.9 http://www.sitemaps.org/schemas/sitemap/0.9/sitemapindex.xsd">
	<sitemap>
		<loc>{{.URL}}/sitemap-latest.xml</loc>
		<lastmod>2021-08-05T07:35-04:00</lastmod>
	</sitemap>
</sitemapindex>`
		if r.URL.Path == "/sitemap.xml" {
			tmpl, err := template.New("").Parse(maindata)
			if err != nil {
				panic(err)
			}
			err = tmpl.Execute(rw, data)
			if err != nil {
				panic(err)
			}
			return
		} else if r.URL.Path == "sitemap-latest.xml" {
			blob := `
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance" xmlns:image="http://www.google.com/schemas/sitemap-image/1.1" xmlns:video="http://www.google.com/schemas/sitemap-video/1.1" xmlns:news="http://www.google.com/schemas/sitemap-news/0.9" xsi:schemaLocation="http://www.sitemaps.org/schemas/sitemap/0.9 http://www.sitemaps.org/schemas/sitemap/0.9/sitemap.xsd">
	<url>
		<loc>{{.URL}}/some-page</loc><lastmod>2021-07-09</lastmod>
	</url>`
			tmpl, err := template.New("").Parse(blob)
			if err != nil {
				panic(err)
			}
			err = tmpl.Execute(rw, data)
			if err != nil {
				panic(err)
			}
			return
		}
	}
	return handler
}

func TestSitemap(t *testing.T) {
	// s := "https://www.npr.org/live-updates/sitemap.xml"
	// s = "https://www.goodreads.com/siteindex.author.xml"
	l := newLocalListener()
	h := sitemapHandler(fmt.Sprintf("http://%s", l.Addr()))
	srv := newserver(l, h)
	srv.Start()
	HttpClient = srv.Client()
	s := srv.URL + "/sitemap.xml"
	sm, err := GetSitemap(s)
	if err != nil {
		t.Fatal(err)
	}
	if sm == nil {
		t.Fatal("got nil sitempa")
	}
	// TODO call sm.FillContents(context.Background(), 1)
}

func TestURLKey(t *testing.T) {
	link := "https://ar.wikipedia.org/wiki/%D9%85%D9%88%D8%B3%D9%88%D8%B9%D8%A9"
	u, err := url.Parse(link)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte("visited_" + link)
	got := urlKey(u)
	if !bytes.Equal(got, want) {
		t.Errorf("wrong result: got %q, want %q", got, want)
	}
}

func TestFetch(t *testing.T) {
	// t.Skip()
	t.Run("Fetch en.wikipedia", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(testHTTPHandler))
		defer srv.Close()
		HttpClient = srv.Client()
		page := NewPageFromString(srv.URL+"/", 0)
		err := page.Fetch()
		if err != nil {
			t.Fatal(err)
		}
		want := 298
		if len(page.Links) != want {
			t.Errorf("wrong number of links: got %d, want %d", len(page.Links), want)
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
