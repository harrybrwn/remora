package web

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type Cache interface {
	Get(string) (io.Reader, error)
	Set(string, io.Reader) error
	Del(string) error
}

type CacheTransport struct {
	Transport http.RoundTripper
	Cache     Cache
}

func (ct *CacheTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	var (
		cancache bool
		key      = fmt.Sprintf("%s.%s", r.Method, r.URL.String())
	)
	switch r.Method {
	case "GET", "HEAD":
		cancache = true
	default:
		cancache = false
	}
	if ct.Transport == nil {
		ct.Transport = http.DefaultTransport
	}
	if !cancache {
		ct.Cache.Del(key)
	}
	resp, err := getResponse(ct.Cache, r, key)
	if err == nil {
		// found in cache
		// TODO check freshness and possibly invalidate cache
		return resp, nil
	}
	// not found in cache, go to server
	resp, err = ct.Transport.RoundTrip(r)
	if err != nil {
		return nil, err
	}
	cacheControl := resp.Header.Get("Cache-Control")
	pragma := strings.ToLower(resp.Header.Get("Pragma"))
	if pragma == "no-cache" || cacheControl == "" {
		return resp, err
	}
	ctrl, err := parseCacheControl(cacheControl)
	if err != nil {
		return resp, nil
	}

	if ctrl.NoCache || ctrl.NoStore {
		return resp, nil
	}

	b, err := dumpResponse(resp)
	if err != nil {
		return resp, err
	}
	err = ct.Cache.Set(key, b)
	return resp, err
}

func getResponse(c Cache, r *http.Request, key string) (*http.Response, error) {
	raw, err := c.Get(key)
	if err != nil {
		return nil, err
	}
	resp, err := http.ReadResponse(bufio.NewReader(raw), r)
	if err != nil {
		return nil, err
	}
	resp.Header.Set("X-From-Cache", "1")
	return resp, nil
}

type scope int

const (
	private scope = iota
	public
)

type Control struct {
	Scope           scope // public or private
	NoStore         bool  // no-store
	NoCache         bool  // no-cache
	NoTransform     bool
	MustRevalidate  bool // must-revalidate
	ProxyRevalidate bool // proxy-revalidate
	OnlyIfCached    bool
	MaxAge          time.Duration // max-age
	SharedMaxAge    time.Duration // s-max-age
}

func parseCacheControl(s string) (*Control, error) {
	var (
		ctrl Control
		b    strings.Builder
		err  error
		l    = len(s)
	)
	for i := 0; i < l; {
		if s[i] == ' ' || s[i] == '\t' || s[i] == '\n' {
			i++
			continue
		} else if s[i] == ',' {
			i++
			goto AddField
		}
		err = b.WriteByte(s[i])
		if err != nil {
			return nil, err
		}
		i++
		if i == l {
			goto AddField
		}
		continue
	AddField:
		f := strings.ToLower(b.String())
		parts := strings.SplitN(f, "=", 2)
		b.Reset()
		switch parts[0] {
		case "public":
			ctrl.Scope = public
		case "private":
			ctrl.Scope = private
		case "no-store":
			ctrl.NoStore = true
		case "no-cache":
			ctrl.NoCache = true
		case "no-transform":
			ctrl.NoTransform = true
		case "must-revalidate":
			ctrl.MustRevalidate = true
		case "proxy-revalidate":
			ctrl.ProxyRevalidate = true
		case "only-if-cached":
			ctrl.OnlyIfCached = true
		case "max-age":
			n, err := strconv.ParseInt(parts[1], 10, 32)
			if err != nil {
				return nil, err
			}
			ctrl.MaxAge = time.Second * time.Duration(n)
		case "s-max-age", "s-maxage":
			n, err := strconv.ParseInt(parts[1], 10, 32)
			if err != nil {
				return nil, err
			}
			ctrl.SharedMaxAge = time.Second * time.Duration(n)
		}
	}
	return &ctrl, nil
}

// Taken from httputil.DumpResponse
func dumpResponse(resp *http.Response) (io.Reader, error) {
	var (
		err error
		b   bytes.Buffer
	)
	save := resp.Body
	savecl := resp.ContentLength
	if resp.Body == nil {
		resp.Body = io.NopCloser(strings.NewReader(""))
	} else {
		save, resp.Body, err = drainBody(resp.Body)
		if err != nil {
			return nil, err
		}
	}
	err = resp.Write(&b)
	resp.Body = save
	resp.ContentLength = savecl
	if err != nil {
		return nil, err
	}
	return &b, nil
}

func drainBody(b io.ReadCloser) (r1, r2 io.ReadCloser, err error) {
	if b == nil || b == http.NoBody {
		// No copying needed. Preserve the magic sentinel meaning of NoBody.
		return http.NoBody, http.NoBody, nil
	}
	var buf bytes.Buffer
	if _, err = buf.ReadFrom(b); err != nil {
		return nil, b, err
	}
	if err = b.Close(); err != nil {
		return nil, b, err
	}
	return io.NopCloser(&buf), io.NopCloser(bytes.NewReader(buf.Bytes())), nil
}
