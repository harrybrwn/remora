package rabbitmq

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
)

type ManagementServer struct {
	Host               string
	Port               int
	Username, Password string
	// TLS should be true if the
	// caller wants to use https
	TLS bool
	c   http.Client
}

func (m *ManagementServer) Queues() ([]Queue, error) {
	queues := make([]Queue, 0)
	return queues, m.do("queues", &queues)
}

func (m *ManagementServer) Channels() ([]Channel, error) {
	channels := make([]Channel, 0)
	return channels, m.do("channels", &channels)
}

func (m *ManagementServer) do(path string, dest interface{}) error {
	req := http.Request{
		Method:     "GET",
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Host:       m.getHost(),
		URL:        m.url(path),
		Header:     make(http.Header),
		Body:       http.NoBody,
		GetBody:    func() (io.ReadCloser, error) { return http.NoBody, nil },
	}
	m.checkAuth()
	req.SetBasicAuth(m.Username, m.Password)
	resp, err := m.c.Do(&req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(dest)
}

func (m *ManagementServer) checkAuth() {
	if m.Username == "" && m.Password == "" {
		m.Username = "guest"
		m.Password = "guest"
	}
}

func (m *ManagementServer) getHost() string {
	return net.JoinHostPort(
		m.Host,
		strconv.FormatInt(int64(m.Port), 10),
	)
}

func (m *ManagementServer) url(p string) *url.URL {
	scheme := "http"
	if m.TLS {
		scheme = "https"
	}
	return &url.URL{
		Scheme: scheme,
		Host:   m.getHost(),
		Path:   path.Join("/api", p, "/"),
	}
}
