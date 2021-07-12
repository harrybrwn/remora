package main

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"testing"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
)

func Test(t *testing.T) {
	ctx := context.Background()
	volume := ConfigVolume{Name: "test-volume"}
	conf := Config{
		Instances: []Instance{
			{Host: "unix:///var/run/docker.sock"},
			// {Host: "10.0.0.202"},
		},
		Volumes: []ConfigVolume{volume},
	}
	dk, err := conf.docker()
	if err != nil {
		t.Fatal(err)
	}

	var b bytes.Buffer
	b.WriteString("this is a test of the volume thing")
	err = uploadToVolume(ctx, dk, []*volumeData{{Volume: volume, Reader: &b}})
	if err != nil {
		fmt.Println("could not upload to volume")
		t.Fatal(err)
	}
	if err = dk.Run(func(host string, c *client.Client) error {
		vols, err := c.VolumeList(ctx, filters.Args{})
		if err != nil {
			t.Error(err)
			return err
		}
		for _, vol := range vols.Volumes {
			if vol.Name == volume.Name {
				// c.VolumeRemove(ctx, vol.Name, false)
			}
			fmt.Println(vol.Name, vol.Labels, vol.Driver)
		}
		println()
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// err = dk.Run(func(host string, c *client.Client) error {
	// 	// images, err := c.ImageList(context.Background(), types.ImageListOptions{})
	// 	// if err != nil {
	// 	// 	t.Error(err)
	// 	// 	return err
	// 	// }
	// 	// for _, img := range images {
	// 	// 	created := time.Unix(img.Created, 0)
	// 	// 	fmt.Println(img.ID, created, img.Size, img.Containers, img.RepoTags)
	// 	// }

	// 	volumes, err := c.VolumeList(ctx, filters.NewArgs(filters.KeyValuePair{
	// 		Key: "name", Value: "helper",
	// 	}))
	// 	if err != nil {
	// 		t.Error(err)
	// 		return err
	// 	}
	// 	fmt.Println("warnings:", len(volumes.Warnings))
	// 	fmt.Println("volumes:", len(volumes.Volumes))
	// 	for _, v := range volumes.Volumes {
	// 		fmt.Println(v.CreatedAt, v.Driver, v.Labels, v.Mountpoint, v.Name, v.Status)
	// 	}
	// 	return nil
	// })
	// if err != nil {
	// 	t.Error(err)
	// }
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

func TestMultiDockerHost(t *testing.T) {
	var (
		ctx   = context.Background()
		hosts = []string{
			client.DefaultDockerHost,
			"10.0.0.101",
			"10.0.0.201",
			"10.0.0.202",
			"10.0.0.203",
		}
	)
	dk, err := newMultiDocker(hosts, client.FromEnv)
	if err != nil {
		t.Fatal(err)
	}
	err = dk.Run(func(host string, c *client.Client) error {
		list, err := c.ImageList(ctx, types.ImageListOptions{})
		if err != nil {
			return err
		}
		if len(list) == 0 {
			t.Error("list should not be zero length")
		}
		return nil
	})
	if err != nil {
		t.Error(err)
	}
}

func TestDockerHost(t *testing.T) {
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
	dk, err := c.docker()
	if err != nil {
		t.Fatal(err)
	}
	instmap := c.HostInstances()
	if len(instmap) == 0 || len(dk.hosts) == 0 {
		t.Error("no hosts or instances found in config")
	}
	if len(dk.hosts) != 2 {
		t.Error("wrong number of hosts")
	}
	if len(instmap) != 2 {
		t.Errorf("wrong number of hosts; got %d, want %d", len(instmap), 2)
	}
	for _, instances := range instmap {
		for _, in := range instances {
			if in.Image != c.Build.Image {
				t.Error("wrong image")
			}
			if in.Command == nil || len(in.Command) == 0 {
				t.Error("all instances should have a command")
			}
		}
	}

	t.Skip("TODO: add a filter functionality to the Config.docker function")
	dk, err = c.docker("10.0.0.2:two", "10.0.0.2:three")
	if err != nil {
		t.Fatal(err)
	}
	instmap = c.HostInstances()
	if len(dk.hosts) != 1 {
		t.Error("wrong number of hosts")
	}
	ins := instmap["10.0.0.2"]
	if len(ins) != 2 {
		t.Error("wrong number of instances")
	}
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
