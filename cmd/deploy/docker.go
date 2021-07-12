package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/client"
)

type multiDocker struct {
	hosts   []string
	opts    []client.Opt
	clients map[string]*client.Client
}

func newMultiDocker(hosts []string, opts ...client.Opt) (*multiDocker, error) {
	dk := &multiDocker{
		hosts:   make([]string, 0, len(hosts)),
		clients: make(map[string]*client.Client),
		opts:    opts,
	}
	// var err error
	// for _, h := range hosts {
	// 	_, err = client.ParseHostURL(h)
	// 	if err != nil {
	// 		h, err = parseDockerHost(h)
	// 		if err != nil {
	// 			return nil, err
	// 		}
	// 	}
	// 	dk.hosts = append(dk.hosts, h)
	// }
	dk.hosts = append(dk.hosts, hosts...)
	return dk, nil
}

func (md *multiDocker) Run(fn func(host string, c *client.Client) error) error {
	var (
		err  error
		wg   sync.WaitGroup
		errs = make(chan error)
	)
	for _, host := range md.hosts {
		if _, ok := md.clients[host]; !ok {
			h := host
			_, err = client.ParseHostURL(h)
			if err != nil {
				h, err = parseDockerHost(h)
				if err != nil {
					return err
				}
			}
			opts := append(md.opts, client.WithHost(h))
			md.clients[host], err = client.NewClientWithOpts(opts...)
			if err != nil {
				return err
			}
		}
	}
	wg.Add(len(md.clients))
	for host, cli := range md.clients {
		go func(host string, cli *client.Client) {
			defer wg.Done()
			err := fn(host, cli)
			if err != nil {
				log.Println(err) // TODO handle this error better
				go func() { errs <- err }()
			}
		}(host, cli)
	}
	wg.Wait()
	defer close(errs)
	return <-errs
}

type docker struct{ wg *sync.WaitGroup }

func (d *docker) Wait() { d.wg.Wait() }
func (d *docker) Cmd() *dockerCommand {
	d.wg.Add(1)
	return &dockerCommand{wg: d.wg}
}

type Command interface {
	Run(context.Context) error
	Args() []string
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

func (cmd *dockerCommand) Args() []string {
	var args []string
	if cmd.host != "" {
		args = append(args, "-H", cmd.host)
	}
	if cmd.config != "" {
		args = append(args, "--config", cmd.config)
	}
	args = append(args, cmd.args...)
	return args
}

const DryRun = false

func (cmd *dockerCommand) Run(ctx context.Context) error {
	defer cmd.wg.Done()
	args := cmd.Args()
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
	Restart(string) RunCommand
}

type runCommand struct {
	*dockerCommand
	image   string
	command []string
}

const (
	NoRestart     = "no"
	OnFailure     = "on-failure"
	AlwaysRestart = "always"
	UnlessStopped = "unless-stopped"
)

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
func (rc *runCommand) Restart(policy string) RunCommand { return rc.append("--restart", policy) }
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

func (rm *rmCommand) Args() []string {
	return rm.rm.Args()
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

var defaultPort = "2375"

func addPortToHost(host string) (string, error) {
	var (
		port string
		err  error
		h    = host
	)
	host, port, err = net.SplitHostPort(host)
	switch e := err.(type) {
	case nil:
		break
	case *net.AddrError:
		if e.Err == "missing port in address" {
			port = defaultPort
			host = h
		} else {
			return "", err
		}
	default:
		fmt.Printf("%[1]T %[1]v\n", err)
		return "", err
	}
	return net.JoinHostPort(host, port), nil
}

func parseDockerHost(host string) (string, error) {
	var err error
	protoAddrParts := strings.SplitN(host, "://", 2)
	if len(protoAddrParts) == 1 {
		protoAddrParts = []string{"tcp", protoAddrParts[0]}
	}
	var basePath string
	proto, addr := protoAddrParts[0], protoAddrParts[1]
	addr, err = addPortToHost(addr)
	if err != nil {
		return "", err
	}
	if proto == "tcp" {
		parsed, err := url.Parse("tcp://" + addr)
		if err != nil {
			return "", err
		}
		addr = parsed.Host
		basePath = parsed.Path
	}
	u := url.URL{
		Scheme: proto,
		Host:   addr,
		Path:   basePath,
	}
	return u.String(), nil
}
