package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/harrybrwn/config"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

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

type Config struct {
	Image     string     `yaml:"image"`
	Build     Build      `yaml:"build"`
	Instances []Instance `yaml:"instances"`
	Hosts     []struct {
		Host      string     `yaml:"host"`
		Image     string     `yaml:"image"`
		Command   []string   `yaml:"command"`
		Volumes   []string   `yaml:"volumes"`
		Build     *Build     `yaml:"build"`
		Instances []Instance `yaml:"instances"`
	} `yaml:"hosts"`
	Builds []Build
}

func (c *Config) interpolate() {
	if c.Build.Image == "" {
		c.Build.Image = c.Image
	}
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

func (c *Config) instances(ids ...string) []Instance {
	c.interpolate()
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

func buildFromInstance(in Instance) Build {
	if in.Build == nil {
		in.Build = new(Build)
	}
	if in.Build.Host == "" {
		in.Build.Host = in.Host
	}
	if in.Build.Image == "" {
		in.Build.Image = in.Image
	}
	b := *in.Build
	b.setDefaults()
	return b
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
	c.interpolate()

	for _, b := range c.Builds {
		b.setDefaults()
		add(b)
	}
	for _, in := range c.Instances {
		b := buildFromInstance(in)
		add(b)
	}
	for _, in := range c.Hosts {
		var b Build
		if in.Build != nil {
			b = *in.Build
		} else {
			b.Host = in.Host
			b.Image = in.Image
			b.setDefaults()
		}
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

func main() {
	var (
		err  error
		conf Config
	)
	config.AddFile("deployment.yml")
	config.AddPath(".")
	config.SetType("yaml")
	config.SetConfig(&conf)
	err = config.ReadConfig()
	if err != nil {
		log.Fatal(errors.Wrap(err, "could not read config file"))
	}

	cli := NewCliRoot(&conf)
	err = cli.Execute()
	if err != nil {
		log.Println(err)
	}
}

func run(ctx context.Context, dk *docker, commands []Command) error {
	if len(commands) == 0 {
		return errors.New("no commands to run")
	}
	for _, cmd := range commands {
		go func(c Command) { handle(c.Run(ctx)) }(cmd)
	}
	dk.Wait()
	return nil
}

func NewCliRoot(conf *Config) *cobra.Command {
	var (
		dk     = &docker{wg: new(sync.WaitGroup)}
		all    bool
		follow bool
	)
	c := &cobra.Command{
		Use: "deploy", Short: "Container deployment tool for the web crawler",
	}

	ps := &cobra.Command{
		Use: "ps", Short: "",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer cancel()
			commands, err := ps(conf.instances(args...), dk, all)
			if err != nil {
				return err
			}
			for _, comm := range commands {
				handle(comm.Run(ctx))
			}
			dk.Wait()
			return nil
		},
	}
	ps.Flags().BoolVarP(&all, "all", "a", false, "list stopped containers as well as other containers")

	logs := &cobra.Command{
		Use: "logs", Short: "Display container logging",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer cancel()
			commands, err := logs(conf.instances(args...), dk, follow)
			if err != nil {
				return err
			}
			return run(ctx, dk, commands)
		},
	}
	logs.Flags().BoolVarP(&follow, "follow", "f", follow, "follow the logs")

	c.AddCommand(
		ps,
		logs,
		&cobra.Command{
			Use: "up", Short: "Run all the containers",
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt)
				defer cancel()
				commands, err := up(conf.instances(args...), dk)
				if err != nil {
					return err
				}
				return run(ctx, dk, commands)
			},
		},
		&cobra.Command{
			Use: "down", Short: "Stop and remove all containers",
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt)
				defer cancel()
				commands, err := down(conf.instances(args...), dk)
				if err != nil {
					return err
				}
				return run(ctx, dk, commands)
			},
		},
		&cobra.Command{
			Use: "build", Short: "Build all the images the deployment depends on",
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt)
				defer cancel()
				commands, err := build(conf.builds(args...), dk)
				if err != nil {
					return err
				}
				return run(ctx, dk, commands)
			},
		},
		&cobra.Command{
			Use: "stop", Short: "Stop all the containers",
			RunE: func(cmd *cobra.Command, args []string) error {
				ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt)
				defer cancel()
				commands, err := stop(conf.instances(args...), dk)
				if err != nil {
					return err
				}
				return run(ctx, dk, commands)
			},
		},
		&cobra.Command{
			Use: "list",
			RunE: func(cmd *cobra.Command, args []string) error {
				for _, inst := range conf.instances(args...) {
					fmt.Println(inst.Host, inst.Image, inst.Name, inst.Command)
				}
				return nil
			},
		},
	)
	return c
}

func SplitVolume(v string) (src, dest string, err error) {
	parts := strings.Split(v, ":")
	if len(parts) != 2 {
		err = errors.New("invalid volume syntax")
		return
	}
	src = parts[0]
	dest = parts[1]
	return
}

func handle(err error) {
	if err != nil {
		fmt.Println("Error:", err.Error())
	}
}

func up(inst []Instance, dk *docker) (commands []Command, err error) {
	if len(inst) == 0 {
		err = errors.New("no instances to deploy")
		return
	}
	for _, in := range inst {
		c := dk.Cmd().WithHost(in.Host).ContainerRun(
			in.Image, in.Command,
		).Hostname(fmt.Sprintf("%s-%s", in.Host, in.Name))
		for _, v := range in.Volumes {
			src, dest, err := SplitVolume(v)
			if err != nil {
				return nil, err
			}
			c.Volume(src, dest)
		}
		commands = append(commands, c.Detach().Name(in.Name))
	}
	return
}

func down(inst []Instance, dk *docker) (commands []Command, err error) {
	if len(inst) == 0 {
		err = errors.New("not instances to take down")
		return
	}
	for _, in := range inst {
		cmd := dk.Cmd().WithHost(in.Host)
		commands = append(commands, cmd.Stop(in.Name).Remove())
	}
	return
}

func build(builds []Build, dk *docker) (commands []Command, err error) {
	if len(builds) == 0 {
		err = errors.New("no builds to execute")
		return
	}
	for _, b := range builds {
		cmd := dk.Cmd().WithHost(b.Host).ImageBuild(b.Image).WithBuild(b)
		commands = append(commands, cmd)
	}
	return
}

func stop(inst []Instance, dk *docker) (commands []Command, err error) {
	for _, in := range inst {
		cmd := dk.Cmd().WithHost(in.Host).Stop(in.Name)
		commands = append(commands, cmd)
	}
	return
}

func ps(inst []Instance, dk *docker, all bool) (commands []Command, err error) {
	hosts := make(map[string]struct{})
	for _, in := range inst {
		_, ok := hosts[in.Host]
		if ok {
			continue
		}
		hosts[in.Host] = struct{}{}
		cmd := dk.Cmd().WithHost(in.Host)
		cmd.args = append(cmd.args, "container", "ls")
		if all {
			cmd.args = append(cmd.args, "--no-trunc", "--all")
		}
		commands = append(commands, cmd)
	}
	return
}

func logs(inst []Instance, dk *docker, follow bool) (commands []Command, err error) {
	for _, in := range inst {
		cmd := dk.Cmd().WithHost(in.Host)
		cmd.args = append(cmd.args, "container", "logs", in.Name)
		if follow {
			cmd.args = append(cmd.args, "-f")
		}
		commands = append(commands, cmd)
	}
	return
}

type docker struct{ wg *sync.WaitGroup }

func (d *docker) Wait() { d.wg.Wait() }
func (d *docker) Cmd() *dockerCommand {
	d.wg.Add(1)
	return &dockerCommand{wg: d.wg}
}

type Command interface {
	Run(context.Context) error
}

type dockerCommand struct {
	wg     *sync.WaitGroup
	out    io.ReadWriter
	host   string
	config string
	args   []string
}

func (cmd *dockerCommand) WithHost(h string) *dockerCommand {
	cmd.host = h
	return cmd
}
func (cmd *dockerCommand) WithConfig(file string) *dockerCommand {
	cmd.config = file
	return cmd
}
func (cmd *dockerCommand) WithOut(rw io.ReadWriter) *dockerCommand {
	cmd.out = rw
	return cmd
}

const DryRun = false

func (cmd *dockerCommand) Run(ctx context.Context) error {
	defer cmd.wg.Done()
	var args []string
	if cmd.host != "" {
		args = append(args, "-H", cmd.host)
	}
	if cmd.config != "" {
		args = append(args, "--config", cmd.config)
	}
	args = append(args, cmd.args...)
	if DryRun {
		fmt.Println("docker", args)
		return nil
	}
	if cmd.out == nil {
		cmd.out = os.Stdout
	}
	c := exec.Command("docker", args...)
	c.Stdout, c.Stderr = cmd.out, cmd.out
	err := c.Run()
	switch e := err.(type) {
	case *exec.ExitError:
		// TODO handle this error in some interesting way
		return e
	}
	return err
}

type BuildCommand interface {
	Command
	WithDockerfile(string) BuildCommand
	WithContext(string) BuildCommand
	WithBuild(Build) BuildCommand
}

type buildCommand struct {
	*dockerCommand
	Dockerfile string
	Context    string
}

func (cmd *dockerCommand) ImageBuild(image string) *buildCommand {
	cmd.args = append(cmd.args, "image", "build", "-t", image)
	return &buildCommand{
		dockerCommand: cmd,
		Dockerfile:    "./Dockerfile",
		Context:       ".",
	}
}

func (bc *buildCommand) WithDockerfile(file string) BuildCommand {
	if bc.Dockerfile != "" {
		bc.Dockerfile = file
	}
	return bc
}
func (bc *buildCommand) WithContext(dir string) BuildCommand {
	if dir != "" {
		bc.Context = dir
	}
	return bc
}
func (bc *buildCommand) WithBuild(build Build) BuildCommand {
	return bc.WithContext(build.Context).WithDockerfile(build.Dockerfile)
}

func (bc *buildCommand) Run(ctx context.Context) error {
	bc.args = append(bc.args, "-f", bc.Dockerfile, bc.Context)
	return bc.dockerCommand.Run(ctx)
}

type RunCommand interface {
	Command

	ContainerRun(image string, command []string) RunCommand
	Detach() RunCommand
	Name(string) RunCommand
	Rm() RunCommand
	Hostname(string) RunCommand
	Memory(string) RunCommand
	Volume(src, dest string) RunCommand
	Env(key, val string) RunCommand
}

type runCommand struct {
	*dockerCommand
	image   string
	command []string
}

func (cmd *dockerCommand) ContainerRun(image string, command []string) RunCommand {
	cmd.args = append(cmd.args, "container", "run")
	return &runCommand{
		dockerCommand: cmd,
		image:         image,
		command:       command,
	}
}
func (rc *runCommand) append(s ...string) *runCommand {
	rc.args = append(rc.args, s...)
	return rc
}
func (rc *runCommand) Detach() RunCommand                  { return rc.append("-d") }
func (rc *runCommand) Name(n string) RunCommand            { return rc.append("--name", n) }
func (rc *runCommand) Rm() RunCommand                      { return rc.append("--rm") }
func (rc *runCommand) Hostname(hostname string) RunCommand { return rc.append("--hostname", hostname) }
func (rc *runCommand) Memory(mem string) RunCommand        { return rc.append("-m", mem) }
func (rc *runCommand) VolumeStr(v string) RunCommand       { return rc.append("-v", v) }
func (rc *runCommand) Volume(src, dst string) RunCommand {
	return rc.append("-v", fmt.Sprintf("%s:%s", src, dst))
}
func (rc *runCommand) Env(key, value string) RunCommand {
	return rc.append("-e", fmt.Sprintf("%s=%s", key, value))
}
func (rc *runCommand) Run(ctx context.Context) error {
	rc.args = append(rc.args, rc.image)
	rc.args = append(rc.args, rc.command...)
	return rc.dockerCommand.Run(ctx)
}

type StopCommand interface {
	Command
	Remove() RmCommand
}

type RmCommand interface {
	Command
	Force() RmCommand
	Link() RmCommand
	Volumes() RmCommand
}

type stopCommand struct {
	*dockerCommand
	name string
}

func (cmd *dockerCommand) Stop(name string) *stopCommand {
	cmd.args = append(cmd.args, "container", "stop", name)
	return &stopCommand{
		dockerCommand: cmd,
		name:          name,
	}
}

// Time sets the seconds to wait for stop before killing the container
func (sc *stopCommand) Time(t time.Duration) StopCommand {
	s := strconv.FormatInt(int64(t.Seconds()), 10)
	sc.args = append(sc.args, s)
	return sc
}

func (sc *stopCommand) Remove() RmCommand {
	rm := &dockerCommand{
		wg:     sc.wg,
		out:    sc.out,
		host:   sc.host,
		config: sc.config,
		args:   []string{"container", "rm", sc.name},
	}
	return &rmCommand{
		stop: sc,
		rm:   rm,
	}
}

type rmCommand struct {
	stop *stopCommand
	rm   *dockerCommand
}

// Force will for the removal of the container
func (rm *rmCommand) Force() RmCommand {
	rm.rm.args = append(rm.rm.args, "--force")
	return rm
}

// Link will remove the link
func (rm *rmCommand) Link() RmCommand {
	rm.rm.args = append(rm.rm.args, "--link")
	return rm
}

// Volumes will remove anonymous volumes associated with the container
func (rm *rmCommand) Volumes() RmCommand {
	rm.rm.args = append(rm.rm.args, "--volumes")
	return rm
}

func (rm *rmCommand) StopContainer() error {
	return rm.stop.Run(context.Background())
}

func (rm *rmCommand) Run(ctx context.Context) error {
	rm.rm.wg.Add(1)
	err := rm.stop.Run(ctx)
	if err != nil {
		rm.rm.wg.Done()
		return err
	}
	return rm.rm.Run(ctx)
}
