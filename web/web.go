package web

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"

	"github.com/sirupsen/logrus"
	"github.com/temoto/robotstxt"
)

var (
	ErrSkipURL = errors.New("skip url")

	log = logrus.StandardLogger()
)

type Visitor interface {
	// Filter is called after checking page depth
	// and after checking for a repeated URL.
	Filter(*PageRequest, *url.URL) error

	// Visit is called after a page is fetched.
	Visit(context.Context, *Page)

	// LinkFound is called when a new link is
	// found and popped off of the main queue
	// and before any depth checking or repeat
	// checking.
	LinkFound(*url.URL)
}

func SetLogger(l *logrus.Logger) { log = l }
func GetLogger() *logrus.Logger  { return log }

func GetRobotsTxT(host string) (*robotstxt.RobotsData, error) {
	req := &http.Request{
		Method: "GET",
		Proto:  "HTTP/1.1",
		Host:   host,
		URL: &url.URL{
			Scheme: "https",
			Host:   host,
			Path:   "/robots.txt",
		},
		Header: http.Header{
			"Pragma": {"No-Cache"},
		},
		Body:    http.NoBody,
		GetBody: defaultGetBody,
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return robotstxt.FromStatusAndBytes(resp.StatusCode, b)
}

// RequestQueue is a queue for pages
type RequestQueue interface {
	Enqueue(*PageRequest) error
	Dequeue() (*PageRequest, error)
	Close() error
	Size() int64
}
