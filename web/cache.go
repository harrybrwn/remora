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

type CachingTransport struct {
	Transport http.RoundTripper
	Cache     Cache
}

func (ct *CachingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	var (
		cancache bool
		// TODO is this really the right move?
		key = fmt.Sprintf("%s.%s", r.Method, r.URL.String())
	)
	switch r.Method {
	case "GET", "HEAD":
		cancache = true
	default:
		cancache = false
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
	var ctrl CacheControl
	err = parseCacheControl(cacheControl, &ctrl)
	if err != nil {
		return resp, nil
	}

	if ctrl.NoCache() || ctrl.NoStore() {
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

type CacheControl struct {
	Scope scope // public or private
	// Attrs is a bit field containing no-store, no-cache, no-transform, must-revalidate, proxy-revalidate, and only-if-cached
	Attrs        CacheAttr
	MaxAge       time.Duration // max-age
	SharedMaxAge time.Duration // s-max-age
}

func (ctrl *CacheControl) Init(r *http.Response) error {
	cacheControl := r.Header.Get("Cache-Control")
	return parseCacheControl(cacheControl, ctrl)
}

func (ctrl *CacheControl) NoStore() bool     { return ctrl.Attrs&CacheNoStore == CacheNoStore }
func (ctrl *CacheControl) NoCache() bool     { return ctrl.Attrs&CacheNoCache == CacheNoCache }
func (ctrl *CacheControl) NoTransform() bool { return ctrl.Attrs&CacheNoTransform == CacheNoTransform }
func (ctrl *CacheControl) MustRevalidate() bool {
	return ctrl.Attrs&CacheMustRevalidate == CacheMustRevalidate
}
func (ctrl *CacheControl) ProxyRevalidate() bool {
	return ctrl.Attrs&CacheProxyRevalidate == CacheProxyRevalidate
}
func (ctrl *CacheControl) OnlyIfCached() bool {
	return ctrl.Attrs&CacheOnlyIfCached == CacheOnlyIfCached
}

type scope uint8

const (
	private scope = iota
	public
)

const (
	ScopePrivate = private
	ScopePublic  = public
)

type CacheAttr uint8

const (
	CacheNoStore CacheAttr = 1 << iota
	CacheNoCache
	CacheNoTransform
	CacheMustRevalidate
	CacheProxyRevalidate
	CacheOnlyIfCached
	// CacheNil represents the absence of cache attributes.
	CacheNil CacheAttr = 0
)

func parseCacheControl(s string, ctrl *CacheControl) error {
	var (
		b   strings.Builder
		err error
		l   = len(s)
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
			return err
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
			ctrl.Attrs |= CacheNoStore
		case "no-cache":
			ctrl.Attrs |= CacheNoCache
		case "no-transform":
			ctrl.Attrs |= CacheNoTransform
		case "must-revalidate":
			ctrl.Attrs |= CacheMustRevalidate
		case "proxy-revalidate":
			ctrl.Attrs |= CacheProxyRevalidate
		case "only-if-cached":
			ctrl.Attrs |= CacheOnlyIfCached
		case "max-age":
			n, err := strconv.ParseInt(parts[1], 10, 32)
			if err != nil {
				return err
			}
			ctrl.MaxAge = time.Second * time.Duration(n)
		case "s-max-age", "s-maxage":
			n, err := strconv.ParseInt(parts[1], 10, 32)
			if err != nil {
				return err
			}
			ctrl.SharedMaxAge = time.Second * time.Duration(n)
		}
	}
	return nil
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
