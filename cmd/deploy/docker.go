package main

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
)

type multiDocker struct {
	configs map[string]*HostConfig
	opts    []client.Opt
}

func newMultiDocker(configs map[string]*HostConfig, opts ...client.Opt) *multiDocker {
	return &multiDocker{
		configs: configs,
		opts:    opts,
	}
}

type runFunc func(ctx context.Context, conf *HostConfig, cli *client.Client) error

func (md *multiDocker) Run(
	ctx context.Context,
	fn runFunc,
) error {
	var (
		wg   sync.WaitGroup
		errs = make(chan error)
	)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	wg.Add(len(md.configs))
	go func() {
		wg.Wait()
		close(errs)
	}()

	for host, conf := range md.configs {
		go func(host string, c *HostConfig) {
			defer wg.Done()
			// TODO there is probably a better
			// way to check the context here
			select {
			case <-ctx.Done():
				return
			default:
			}
			h, err := c.dockerURL()
			if err != nil {
				errs <- err
				return
			}
			// TODO is there a race condition on md.opts???
			opts := append(md.opts, client.WithHost(h))
			cli, err := client.NewClientWithOpts(opts...)
			if err != nil {
				errs <- err
				return
			}
			defer cli.Close()
			err = fn(ctx, c, cli)
			if err != nil {
				errs <- err
				return
			}
		}(host, conf)
	}
	return <-errs
}

func (md *multiDocker) ForEachHost(fn func(host string, h *HostConfig) error) {
	for host, h := range md.configs {
		err := fn(host, h)
		if err != nil {
			return
		}
	}
}

func containerLogs(ctx context.Context, dk *multiDocker, instances []Instance, opts types.ContainerLogsOptions) error {
	var (
		wg  sync.WaitGroup
		mu  sync.Mutex
		max int
	)
	ins := make(map[string][]Instance)
	for _, in := range instances {
		a, ok := ins[in.Host]
		if ok {
			ins[in.Host] = append(a, in)
		} else {
			ins[in.Host] = []Instance{in}
		}
	}
	for _, conf := range dk.configs {
		for _, in := range conf.Instances {
			l := len(conf.Host) + len(in.Name) + 1
			if l > max {
				max = l
			}
		}
	}
	colors := newcolorset()

	return dk.Run(ctx, func(ctx context.Context, host *HostConfig, c *client.Client) error {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		list, err := c.ContainerList(ctx, types.ContainerListOptions{})
		if err != nil {
			return err
		}
		var (
			errs             = make(chan error)
			names            = make([]string, 0)
			containers       = make([]types.Container, 0)
			maxprefixlen int = max
		)

		for _, container := range list {
			for _, in := range ins[host.Host] {
				for _, name := range container.Names {
					if strings.Compare(in.Name, name[1:]) == 0 {
						containers = append(containers, container)
						names = append(names, name[1:])
					}
				}
			}
		}
		go func() {
			wg.Wait()
			close(errs)
		}()

		for i, container := range containers {
			wg.Add(1)
			go func(container types.Container, name string) {
				defer wg.Done()
				select {
				case <-ctx.Done():
					return
				default:
				}
				info, err := c.ContainerInspect(ctx, container.ID)
				if err != nil {
					errs <- errors.Wrap(err, "could not get container info")
					return
				}
				rc, err := c.ContainerLogs(ctx, container.ID, opts)
				if err != nil {
					errs <- err
					return
				}
				defer rc.Close()
				if info.Config.Tty {
					_, err = stdcopy.StdCopy(os.Stdout, os.Stderr, rc)
				} else {
					col := colors.New()
					prefix := fmt.Sprintf("%s %s", host.Host, name)
					prefix = col.Sprintf("[%s]%s", prefix, strings.Repeat(" ", maxprefixlen-len(prefix)))
					_, err = readLogsPrefix(os.Stdout, []byte(prefix), rc, &mu)
				}
				if err != nil && err != io.EOF {
					errs <- err
					return
				}
			}(container, names[i])
		}
		return <-errs
	})
}

func readLogsPrefix(
	out io.Writer,
	prefix []byte,
	rc io.ReadCloser,
	mu *sync.Mutex,
) (written int64, err error) {
	const (
		headerLen      = 8
		startingBufLen = 32*1024 + headerLen + 1
	)
	var (
		buffer      = make([]byte, startingBufLen)
		buflen      = len(buffer)
		nr, nw      int
		er, ew      error
		size        int
		n, newwrite int
	)
	for {
		// Make sure a full header is in the buffer
		for nr < headerLen {
			var nr2 int
			nr2, er = rc.Read(buffer[nr:])
			nr += nr2
			if err == io.EOF {
				if nr < headerLen {
					return written, nil
				}
				break
			}
			if er != nil {
				return 0, er
			}
		}

		stream := stdcopy.StdType(buffer[0])
		switch stream {
		case stdcopy.Stdin, stdcopy.Stderr, stdcopy.Stdout:
		case stdcopy.Systemerr:
			fallthrough
		default:
			return 0, fmt.Errorf("unrecognized input header: %d", buffer[0])
		}
		size = int(binary.BigEndian.Uint32(buffer[4:headerLen]))

		if size+headerLen > buflen {
			buffer = append(buffer, make([]byte, size+headerLen-buflen+1)...)
			buflen = len(buffer)
		}
		for nr < size+headerLen {
			var nr2 int
			nr2, er = rc.Read(buffer[nr:])
			nr += nr2
			if er == io.EOF {
				if nr < size+headerLen {
					return written, nil
				}
				break
			}
			if er != nil {
				return 0, er
			}
		}

		if stream == stdcopy.Systemerr {
			return 0, fmt.Errorf("error from docker deamon in log stream: %s", string(buffer[headerLen+size+headerLen]))
		}

		newwrite = 0
		nw = 0
		n = headerLen
		stop := size + headerLen
		for n < stop {
			i := n
			for ; i < stop; i++ {
				if buffer[i] == '\n' {
					i++
					goto WriteLine
				}
			}

		WriteLine:
			mu.Lock()
			_, ew = out.Write(prefix)
			if ew != nil {
				mu.Unlock()
				return 0, ew
			}
			_, ew = out.Write([]byte{' '})
			if ew != nil {
				mu.Unlock()
				return 0, ew
			}
			newwrite, ew = out.Write(buffer[n:i])
			if ew != nil {
				mu.Unlock()
				return 0, ew
			}
			n += i
			nw += newwrite
			mu.Unlock()
		}

		if nw != size {
			return 0, io.ErrShortWrite
		}
		written += int64(nw)
		copy(buffer, buffer[size+headerLen:])
		nr -= size + headerLen
	}
}

func uploadFilesToVolume(
	ctx context.Context,
	c *client.Client,
	volname string,
	files []fs.File,
) error {
	version, err := c.ServerVersion(ctx)
	if err != nil {
		return err
	}
	v, err := c.VolumeCreate(ctx, volume.VolumeCreateBody{
		Name: volname,
	})
	if err != nil {
		return err
	}

	var (
		dst         = fmt.Sprintf("/volume-data/%s", volname)
		volumePaths = fmt.Sprintf("%s:/%s", v.Mountpoint, dst)
	)
	container, err := c.ContainerCreate(
		ctx,
		&container.Config{
			Image: "busybox",
			Cmd: []string{
				"tail",
				"-f",
				"/dev/null",
			},
			Volumes: map[string]struct{}{volumePaths: {}},
		},
		&container.HostConfig{
			Mounts: []mount.Mount{
				{
					Type:        mount.TypeVolume,
					Source:      v.Name,
					Target:      dst,
					ReadOnly:    false,
					Consistency: mount.ConsistencyDefault,
					VolumeOptions: &mount.VolumeOptions{
						DriverConfig: &mount.Driver{},
					},
				},
			},
		},
		&network.NetworkingConfig{},
		&specs.Platform{
			Architecture: version.Arch,
			OS:           version.Os,
			OSVersion:    version.KernelVersion,
		},
		"volume-upload-helper",
	)
	if err != nil {
		return err
	}
	defer c.ContainerRemove(ctx, container.ID, types.ContainerRemoveOptions{
		RemoveVolumes: false,
		Force:         true,
	})
	var (
		buf bytes.Buffer
		w   = tar.NewWriter(&buf)
	)
	err = archiveFiles(w, files)
	if err != nil {
		return err
	}
	return c.CopyToContainer(
		ctx,
		container.ID,
		dst,
		&buf,
		types.CopyToContainerOptions{},
	)
}

func archiveFiles(w *tar.Writer, files []fs.File) error {
	for _, file := range files {
		stat, err := file.Stat()
		if err != nil {
			return err
		}
		head := tar.Header{
			Name:       stat.Name(),
			Mode:       int64(stat.Mode()),
			Size:       stat.Size(),
			Gid:        os.Getgid(),
			Uid:        os.Getuid(),
			ModTime:    stat.ModTime(),
			AccessTime: time.Now(),
			ChangeTime: stat.ModTime(),
		}
		err = w.WriteHeader(&head)
		if err != nil {
			return err
		}
		_, err = io.Copy(w, file)
		if err != nil {
			return err
		}
	}
	return nil
}

/////////////////////////
// BELOW IS DEPRECATED //
/////////////////////////

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

func (bc *dockerCommand) ImagePull(image string) *dockerCommand {
	bc.args = append(bc.args, "pull", image)
	return bc
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
	Interactive() RunCommand
	Term() RunCommand
	Pull(string) RunCommand
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
func (rc *runCommand) Interactive() RunCommand     { return rc.append("-i") }
func (rc *runCommand) Term() RunCommand            { return rc.append("-t") }
func (rc *runCommand) Pull(pull string) RunCommand { return rc.append("--pull", pull) }
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

type RestartCommand interface {
	Command
}

type restartCommand struct {
	*dockerCommand
}

func (cmd *dockerCommand) ContainerRestart(name string) RestartCommand {
	cmd.args = append(cmd.args, "container", "restart", name)
	return &restartCommand{cmd}
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
