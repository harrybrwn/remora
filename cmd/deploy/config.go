package main

import "strings"

type Instance struct {
	Host     string   `yaml:"host"`
	Image    string   `yaml:"image"`
	Name     string   `yaml:"name"`
	Volumes  []string `yaml:"volumes"`
	Command  []string `yaml:"command"`
	Replicas int      `yaml:"replicas"`
	Build    *Build   `yaml:"build"`
}

type Build struct {
	Host       string `yaml:"host"`
	Image      string `yaml:"image"`
	Context    string `yaml:"context"`
	Dockerfile string `yaml:"dockerfile"`
}

func (b *Build) setDefaults() {
	if b.Context == "" {
		b.Context = "."
	}
	if b.Dockerfile == "" {
		b.Dockerfile = "./Dockerfile"
	}
}

type ConfigHost struct {
	Host      string     `yaml:"host"`
	Image     string     `yaml:"image"`
	Command   []string   `yaml:"command"`
	Volumes   []string   `yaml:"volumes"`
	Instances []Instance `yaml:"instances"`
}

type ConfigVolume struct {
	Name  string   `yaml:"name"`
	Files []string `yaml:"files"`
}

type Config struct {
	Build     Build        `yaml:"build"`
	Instances []Instance   `yaml:"instances"`
	Hosts     []ConfigHost `yaml:"hosts"`
	Builds    []Build
	Volumes   []ConfigVolume
}

func (c *Config) interpolate() {
	for i := range c.Hosts {
		if c.Hosts[i].Image == "" {
			c.Hosts[i].Image = c.Build.Image
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
		}
	}
	for i := range c.Instances {
		if c.Instances[i].Name == "" {
			continue
		}
		if c.Instances[i].Image == "" {
			c.Instances[i].Image = c.Build.Image
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
		panic("invalid filter string") // TODO handle this
	}
	return &instanceFilter{
		Host: parts[0],
		Name: parts[1],
	}
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
	type pair struct {
		host, name string
	}
	res := make([]Instance, 0)
	m := make(map[pair]Instance)
	for _, inst := range c.Instances {
		p := pair{host: inst.Host, name: inst.Name}
		m[p] = inst
	}
	for _, h := range c.Hosts {
		for _, inst := range h.Instances {
			p := pair{host: inst.Host, name: inst.Name}
			m[p] = inst
		}
	}
	for _, id := range ids {
		parts := strings.Split(id, ":")
		if len(parts) != 2 {
			continue
		}
		p := pair{host: parts[0], name: parts[1]}
		inst, ok := m[p]
		if !ok {
			continue
		}
		res = append(res, inst)
	}
	return res
}

func shouldFilterOut(i *Instance, filters []Filter) bool {
	for _, f := range filters {
		if !f.Matches(i.Host, i.Name) {
			return true
		}
	}
	return false
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
	l := len(arr)
	arr[index] = arr[l]
	return arr[:l]
}

func (c *Config) hosts() []string {
	var (
		ok bool
		i  = 0
		m  = make(map[string]struct{})
	)
	for _, in := range c.Instances {
		if _, ok = m[in.Host]; !ok {
			m[in.Host] = struct{}{}
		}
	}
	for _, h := range c.Hosts {
		if _, ok = m[h.Host]; !ok {
			m[h.Host] = struct{}{}
		}
	}
	hosts := make([]string, len(m))
	for host := range m {
		hosts[i] = host
		i++
	}
	return hosts
}

func (c *Config) docker(filters ...string) (*multiDocker, error) {
	dk, err := newMultiDocker(c.hosts())
	if err != nil {
		return nil, err
	}
	return dk, nil
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
		b.setDefaults()
		add(b)
	}
	for _, in := range c.Instances {
		b := buildFromInstance(in)
		add(b)
	}
	for _, in := range c.Hosts {
		b := Build{Host: in.Host, Image: in.Image}
		b.setDefaults()
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
