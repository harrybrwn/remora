package rabbitmq

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
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

func (m *ManagementServer) SetClient(c http.Client) { m.c = c }

func (m *ManagementServer) Queues() ([]Queue, error) {
	queues := make([]Queue, 0)
	return queues, m.do("GET", "queues", &queues)
}

func (m *ManagementServer) Channels() ([]Channel, error) {
	channels := make([]Channel, 0)
	return channels, m.do("GET", "channels", &channels)
}

func (m *ManagementServer) Connections() ([]*Connection, error) {
	conns := make([]*Connection, 0)
	return conns, m.do("GET", "connections", &conns)
}

func (m *ManagementServer) GetConnection(name string) (*Connection, error) {
	var c Connection
	return &c, m.do("GET", fmt.Sprintf("connections/%s", name), &c)
}

// Get a connection given a user provided connection name. RabbitMQ connections
// have a default name but you have the option to set a "connection_name" field
// in the properties sent with every connection request. This function will
// perform a linear search of all the connections to find the one that matches
// the given user provided name.
func (m *ManagementServer) GetConnectionByUserProvidedName(name string) (*Connection, error) {
	conns, err := m.Connections()
	if err != nil {
		return nil, err
	}
	for _, c := range conns {
		if c.UserProvidedName == name {
			return c, nil
		}
	}
	return nil, fmt.Errorf("no connection named %q", name)
}

func (m *ManagementServer) CloseConnection(
	name string,
	reason ...string,
) error {
	var body bytes.Buffer
	err := json.NewEncoder(&body).Encode(map[string]string{
		"name":   name,
		"reason": strings.Join(reason, " "),
	})
	if err != nil {
		return err
	}
	path := fmt.Sprintf("connections/%s", name)
	return m.do(
		"DELETE", path, nil,
		withHeader("Content-Type", "application/json"),
		withCloseReason(reason),
		withBody(&body),
	)
}

func (m *ManagementServer) Overview() (*Overview, error) {
	o := Overview{}
	return &o, m.do("GET", "overview", &o)
}

type option func(*http.Request)

func withCloseReason(reasons []string) option {
	return func(r *http.Request) {
		if len(reasons) == 0 {
			return
		}
		r.Header.Set("X-Reason", strings.Join(reasons, " "))
	}
}

func withHeader(name, value string) option {
	return func(r *http.Request) { r.Header.Set(name, value) }
}

func withBody(reader io.Reader) option {
	return func(r *http.Request) {
		body := io.NopCloser(reader)
		r.Body = body
		r.GetBody = func() (io.ReadCloser, error) { return body, nil }
	}
}

func (m *ManagementServer) do(method, path string, dest interface{}, opts ...option) error {
	req := http.Request{
		Method:     method,
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
	for _, o := range opts {
		o(&req)
	}
	resp, err := m.c.Do(&req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNoContent {
		return nil
	}
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	var e Error
	err = json.Unmarshal(b, &e)
	if err == nil && len(e.Err) != 0 && len(e.Reason) != 0 {
		return &e
	}
	if dest == nil {
		return nil
	}
	return json.Unmarshal(b, dest)
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
