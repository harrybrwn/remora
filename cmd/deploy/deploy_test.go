package main

import (
	"fmt"
	"net"
	"testing"

	"github.com/docker/docker/client"
	"github.com/harrybrwn/config"
)

func TestDockerHost(t *testing.T) {
	var conf Config
	config.AddPath(".")
	config.AddFile("../../deployment.yml")
	config.SetType("yaml")
	config.SetConfig(&conf)
	if err := config.ReadConfigFile(); err != nil {
		t.Fatal(err)
	}
	conf.interpolate()
}

func TestDockerURL(t *testing.T) {
	want := "tcp://10.0.0.1:2375"
	got, err := parseDockerHost("10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("wrong host; got %s, want %s", got, want)
	}
}

func TestParseDockerHost(t *testing.T) {
	for _, host := range []string{
		"192.168.0.100",
		"192.169.1.200:2375",
		"tcp://10.0.0.123",
		"tcp://10.0.0.123:2375",
		"unix:///var/local/docker/socket",
		"tcp://10.0.0.5",
		"10.0.0.5:2375",
	} {
		dockerhost, err := parseDockerHost(host)
		if err != nil {
			t.Error(err)
		}
		u, err := client.ParseHostURL(dockerhost)
		if err != nil {
			t.Error(err)
			continue
		}
		_, port, err := net.SplitHostPort(u.Host)
		if err != nil {
			t.Error(err)
			continue
		}
		if u.Scheme != "unix" && port != defaultPort {
			t.Error("wrong default port")
		}
	}
}

func TestAddPortToHost(t *testing.T) {
	for _, host := range []string{
		"192.168.0.100",
		"192.169.1.200",
	} {
		h, err := addPortToHost(host)
		if err != nil {
			t.Error(err)
			continue
		}
		want := fmt.Sprintf("%s:%s", host, defaultPort)
		if h != want {
			t.Errorf("wrong result; want %q, got %q", want, h)
		}
	}
}

func TestConfig_interpolate(t *testing.T) {
	c := Config{
		Build: Build{Image: "testing", Context: "."},
		Instances: []Instance{
			{Host: "localhost", Name: "0", Command: []string{"ls", "-la"}},
		},
		Hosts: []ConfigHost{
			{
				Host: "localhost", Command: []string{"ls", "-la"},
				Instances: []Instance{{Name: "1"}, {Name: "2"}, {Name: "3"}},
			},
		},
		Builds: []Build{{Dockerfile: "./other.dockerfile"}},
	}
	c.interpolate()

	for _, in := range c.Instances {
		if in.Image != c.Build.Image {
			t.Errorf("wrong image; want %q, got %q", c.Build.Image, in.Image)
		}
	}
	for _, host := range c.Hosts {
		if host.Image != c.Build.Image {
			t.Errorf("wrong image; want %q, got %q", c.Build.Image, host.Image)
		}
		for _, in := range host.Instances {
			if in.Image != c.Build.Image {
				t.Errorf("wrong image; want %q, got %q", c.Build.Image, in.Image)
			}
			if !arrEq(host.Command, in.Command) {
				t.Error("host instance should have inherited the command")
			}
		}
	}
	for _, build := range c.Builds {
		if build.Context != c.Build.Context {
			t.Error("did not interpolate an empty build context")
		}
	}
}

func TestConfig_docker(t *testing.T) {
	c := Config{
		Build: Build{Image: "testing:1.0", Context: ".", Dockerfile: "./Dockerfile"},
		Hosts: []ConfigHost{
			{
				Host:    "10.0.0.1",
				Command: []string{"ls", "-l", "/usr/bin"},
				Instances: []Instance{
					{Name: "1"}, {Name: "2"}, {Name: "3"},
				},
			},
			{
				Host:    "10.0.0.2",
				Command: []string{"ping", "google.com"},
				Instances: []Instance{
					{Name: "one"},
					{Name: "two"},
					{Name: "three"},
				},
			},
		},
	}
	c.interpolate()
}

func arrEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
