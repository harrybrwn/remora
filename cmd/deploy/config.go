package main

import (
	"strings"

	"github.com/docker/docker/client"
	"github.com/harrybrwn/remora/internal/deploy"
)

type (
	Instance     = deploy.Instance
	Build        = deploy.Build
	ConfigHost   = deploy.ConfigHost
	UploadConfig = deploy.UploadConfig
)

type Config struct {
	Image     string         `yaml:"image"`
	Volumes   []string       `yaml:"volumes"`
	Build     Build          `yaml:"build"`
	Instances []Instance     `yaml:"instances"`
	Hosts     []ConfigHost   `yaml:"hosts"`
	Builds    []Build        `yaml:"builds"`
	Uploads   []UploadConfig `yaml:"uploads"`
}

func (c *Config) interpolate() {
	if c.Build.Image == "" {
		c.Build.Image = c.Image
	}
	for i := range c.Hosts {
		if c.Hosts[i].Image == "" {
			c.Hosts[i].Image = c.Image
		}
		h := c.Hosts[i]
		for j := range c.Hosts[i].Instances {
			if c.Hosts[i].Instances[j].Host == "" {
				c.Hosts[i].Instances[j].Host = h.Host
			}
			if c.Hosts[i].Instances[j].Image == "" {
				c.Hosts[i].Instances[j].Image = h.Image
			}
			if len(c.Hosts[i].Instances[j].Command) == 0 {
				c.Hosts[i].Instances[j].Command = h.Command
			}
			if len(c.Hosts[i].Instances[j].Volumes) == 0 {
				c.Hosts[i].Instances[j].Volumes = h.Volumes
			}
			c.interpolateVolumes(&c.Hosts[i].Instances[j])
		}
	}
	for i := range c.Instances {
		c.interpolateVolumes(&c.Instances[i])
		if c.Instances[i].Name == "" {
			continue
		}
		if c.Instances[i].Image == "" {
			c.Instances[i].Image = c.Image
		}
	}
	for i := range c.Builds {
		if c.Builds[i].Image == "" {
			c.Builds[i].Image = c.Build.Image
		}
		if c.Builds[i].Context == "" {
			c.Builds[i].Context = c.Build.Context
		}
		if c.Builds[i].Dockerfile == "" {
			c.Builds[i].Dockerfile = c.Build.Dockerfile
		}
	}
}

func (c *Config) interpolateVolumes(in *Instance) {
	var vols = in.Volumes[:]
	for _, volouter := range c.Volumes {
		// If config volume is not in the instsance
		// volume list then add it
		for _, v := range vols {
			if volouter == v {
				goto found
			}
		}
		in.Volumes = append(in.Volumes, volouter)
	found:
	}
}

type Filter interface {
	Matches(host, name string) bool
	HostOK(string) bool
	NameOK(string) bool
}

func Filters(filters []string) []Filter {
	f := make([]Filter, len(filters))
	for i, s := range filters {
		f[i] = newFilter(s)
	}
	return f
}

type instanceFilter struct {
	Host, Name string
}

func (f *instanceFilter) Matches(host, name string) bool {
	return f.HostOK(host) && f.NameOK(name)
}
func (f *instanceFilter) HostOK(host string) bool {
	return f.Host == "*" || host == f.Host
}
func (f *instanceFilter) NameOK(name string) bool {
	return f.Name == "*" || name == f.Name
}

func newFilter(s string) *instanceFilter {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) < 2 {
		parts = append(parts, "*")
	}
	return &instanceFilter{
		Host: parts[0],
		Name: parts[1],
	}
}

type HostConfig struct {
	Host      string
	Builds    []Build
	Instances []Instance
}

func (hc *HostConfig) dockerURL() (string, error) {
	h := hc.Host
	_, err := client.ParseHostURL(h)
	if err != nil {
		h, err = parseDockerHost(h)
		if err != nil {
			return "", err
		}
	}
	return h, nil
}

func (c *Config) HostConfigs(filters ...Filter) map[string]*HostConfig {
	var (
		builds  = make(map[Build]struct{})
		m       = make(map[string]*HostConfig)
		newconf = func(h string) *HostConfig {
			return &HostConfig{Host: h, Builds: make([]Build, 0), Instances: make([]Instance, 0)}
		}
	)

	for _, build := range c.Builds {
		build.InitDefaults()
		l, ok := m[build.Host]
		if !ok {
			l = newconf(build.Host)
			m[build.Host] = l
		}
		if _, ok = builds[build]; !ok {
			l.Builds = append(l.Builds, build)
		}
	}
	for _, in := range c.Instances {
		l, ok := m[in.Host]
		if !ok {
			l = newconf(in.Host)
			m[in.Host] = l
		}
		l.Instances = append(l.Instances, in)
		build := buildFromInstance(in)
		build.InitDefaults()
		if _, ok = builds[build]; !ok {
			l.Builds = append(l.Builds, build)
		}
	}
	for _, h := range c.Hosts {
		l, ok := m[h.Host]
		if !ok {
			l = newconf(h.Host)
			m[h.Host] = l
		}
		l.Instances = append(l.Instances, h.Instances...)
		build := Build{Host: h.Host, Image: h.Image}
		build.InitDefaults()
		if _, ok = builds[build]; !ok {
			l.Builds = append(l.Builds, build)
		}
	}
	// apply filters
	for host, conf := range m {
		for _, f := range filters {
			if !f.HostOK(host) {
				delete(m, host)
				continue
			}

			l := len(conf.Instances)
			ins := conf.Instances
			for i := 0; i < l; i++ {
				if !f.NameOK(ins[i].Name) {
					ins = remove(i, ins)
					l--
					i--
				}
			}
			conf.Instances = ins
		}
	}
	return m
}

func (c *Config) instances(ids ...string) []Instance {
	if len(ids) > 0 {
		return c.findInstances(ids)
	}
	inst := make([]Instance, 0, len(c.Instances)+len(c.Hosts))
	inst = append(inst, c.Instances...)
	for _, h := range c.Hosts {
		inst = append(inst, h.Instances...)
	}
	return inst
}

func (c *Config) findInstances(ids []string) []Instance {
	var (
		res     = make([]Instance, 0)
		filters = Filters(ids)
	)
	for _, in := range c.Instances {
		for _, f := range filters {
			if f.Matches(in.Host, in.Name) {
				res = append(res, in)
			}
		}
	}
	// oof
	for _, h := range c.Hosts {
		for _, in := range h.Instances {
			for _, f := range filters {
				if f.Matches(in.Host, in.Name) {
					res = append(res, in)
				}
			}
		}
	}
	return res
}

func (c *Config) HostInstances(filters ...Filter) map[string][]Instance {
	m := make(map[string][]Instance)
	for _, in := range c.Instances {
		l, ok := m[in.Host]
		if !ok {
			l = []Instance{}
		}
		m[in.Host] = append(l, in)
	}
	for _, h := range c.Hosts {
		l, ok := m[h.Host]
		if !ok {
			l = []Instance{}
		}
		m[h.Host] = append(l, h.Instances...)
	}

	// apply filters
	for _, f := range filters {
		for host, instances := range m {
			if !f.HostOK(host) {
				delete(m, host)
			}
			for i := range instances {
				if !f.NameOK(instances[i].Name) {
					m[host] = remove(i, m[host])
				}
			}
		}
	}
	return m
}

func remove(index int, arr []Instance) []Instance {
	l := len(arr) - 1
	arr[index] = arr[l]
	return arr[:l]
}

func (c *Config) docker(filters ...string) *multiDocker {
	dk := newMultiDocker(c.HostConfigs(Filters(filters)...))
	return dk
}

func (c *Config) builds(hosts ...string) []Build {
	var (
		builds = make([]Build, 0, len(c.Builds))
		m      = make(map[Build]struct{})
		add    = func(b Build) {
			_, ok := m[b]
			if ok {
				return
			}
			m[b] = struct{}{}
			builds = append(builds, b)
		}
	)

	for _, b := range c.Builds {
		b.InitDefaults()
		add(b)
	}
	for _, in := range c.Instances {
		b := buildFromInstance(in)
		add(b)
	}
	for _, in := range c.Hosts {
		b := Build{Host: in.Host, Image: in.Image}
		b.InitDefaults()
		add(b)
	}

	if len(hosts) != 0 {
		// Yes, yes, I know this is awful. Just look away.
		// Neither of theses arrays will get very big in practice
		newbuilds := make([]Build, 0, len(hosts))
		for _, b := range builds {
			for _, h := range hosts {
				if b.Host != h {
					continue
				}
				newbuilds = append(newbuilds, b)
			}
		}
		return newbuilds
	}
	return builds
}

func buildFromInstance(in Instance) Build {
	var b Build
	if in.Build != nil {
		b = *in.Build
	}
	if b.Host == "" {
		b.Host = in.Host
	}
	if b.Image == "" {
		b.Image = in.Image
	}
	b.InitDefaults()
	return b
}
