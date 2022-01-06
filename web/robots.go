package web

import (
	"context"
	"io"
	"net/http"
	"net/url"
	sync "sync"

	"github.com/temoto/robotstxt"
)

// RobotsController describes an interface for some structure that will check if
// the robots.txt file will allow us to crawl a given url.
type RobotsController interface {
	// ShouldSkip will return true given a url an the RobotsController is in the
	// robots.txt file.
	ShouldSkip(*url.URL) bool
}

func NewRobotCtrl(ctx context.Context, host string, agents []string) (*RobotCtrl, error) {
	data, err := GetRobotsTxT(ctx, host)
	if err != nil {
		return nil, err
	}
	return &RobotCtrl{
		UserAgents: agents,
		data:       data,
	}, nil
}

// RobotCtrl is a concurrent safe wrapper for checking if a url is in a
// robots.txt file.
type RobotCtrl struct {
	UserAgents []string
	mu         sync.Mutex
	data       *robotstxt.RobotsData
}

// ShouldSkip will return true given a url an the Robot
func (ctrl *RobotCtrl) ShouldSkip(u *url.URL) bool {
	ctrl.mu.Lock()
	defer ctrl.mu.Unlock()
	for _, agent := range ctrl.UserAgents {
		if !ctrl.data.TestAgent(u.Path, agent) {
			return true
		}
	}
	return false
}

func AllowAll() *robotstxt.RobotsData {
	r, _ := robotstxt.FromBytes(nil)
	return r
}

func GetRobotsTxT(ctx context.Context, host string) (*robotstxt.RobotsData, error) {
	req := &http.Request{
		Method:     "GET",
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Host:       host,
		URL: &url.URL{
			Scheme: "https",
			Host:   host,
			Path:   "/robots.txt",
		},
		Header:  http.Header{},
		Body:    http.NoBody,
		GetBody: defaultGetBody,
	}
	// TODO don't use a global http client
	resp, err := HttpClient.Do(req.WithContext(ctx))
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
