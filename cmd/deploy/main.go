package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"sync"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/harrybrwn/config"
	specs "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

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
	b.setDefaults()
	return b
}

func main() {
	var (
		err  error
		conf Config
		ctx  = context.Background()
	)
	config.AddFile("deployment.yml")
	config.AddPath(".")
	config.SetType("yaml")
	config.SetConfig(&conf)
	err = config.ReadConfig()
	if err != nil {
		log.Fatal(errors.Wrap(err, "could not read config file"))
	}
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()

	cli := NewCliRoot(&conf)
	err = cli.ExecuteContext(ctx)
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
		Use:   "deploy",
		Short: "Container deployment tool for the web crawler",
	}

	ps := &cobra.Command{
		Use: "ps", Short: "",
		RunE: func(cmd *cobra.Command, args []string) error {
			conf.interpolate()
			commands, err := ps(conf.instances(args...), dk, all)
			if err != nil {
				return err
			}
			for _, comm := range commands {
				handle(comm.Run(cmd.Context()))
			}
			dk.Wait()
			return nil
		},
	}
	ps.Flags().BoolVarP(&all, "all", "a", false, "list stopped containers as well as other containers")

	logs := &cobra.Command{
		Use: "logs", Short: "Display container logging",
		RunE: func(cmd *cobra.Command, args []string) error {
			conf.interpolate()
			commands, err := logs(conf.instances(args...), dk, follow)
			if err != nil {
				return err
			}
			return run(cmd.Context(), dk, commands)
		},
	}
	logs.Flags().BoolVarP(&follow, "follow", "f", follow, "follow the logs")

	build := &cobra.Command{
		Use: "build", Short: "Build all the images the deployment depends on",
		RunE: func(cmd *cobra.Command, args []string) error {
			conf.interpolate()
			builds := conf.builds(args...)
			commands, err := build(builds, dk)
			if err != nil {
				return err
			}
			return run(cmd.Context(), dk, commands)
		},
	}

	c.AddCommand(
		ps,
		logs,
		// newLogsCmd(conf),
		build,
		newInstancesCmd(conf, "up", "Run all the containers", up),
		newInstancesCmd(conf, "down", "Stop and remove all containers", down),
		newInstancesCmd(conf, "stop", "Stop all the containers", stop),
		&cobra.Command{
			Use: "list",
			RunE: func(cmd *cobra.Command, args []string) error {
				conf.interpolate()
				instances := conf.instances(args...)
				builds := conf.builds(args...)
				fmt.Println("Instances:")
				for _, in := range instances {
					fmt.Println(" ", in.Host, in.Image, in.Name, in.Command)
				}
				fmt.Println("Builds:")
				for _, b := range builds {
					fmt.Println(" ", b.Host, b.Image, b.Dockerfile, b.Context)
				}
				fmt.Printf("%d instances, %d builds\n", len(instances), len(builds))
				return nil
			},
		},
	)
	return c
}

func newInstancesCmd(conf *Config, use, short string, fn func([]Instance, *docker) ([]Command, error)) *cobra.Command {
	var (
		dk = &docker{wg: new(sync.WaitGroup)}
	)
	return &cobra.Command{
		Use: use, Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			conf.interpolate()
			commands, err := fn(conf.instances(args...), dk)
			if err != nil {
				return err
			}
			return run(cmd.Context(), dk, commands)
		},
	}
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
		c = c.Restart(UnlessStopped)
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

func newLogsCmd(conf *Config) *cobra.Command {
	var (
		flags = types.ContainerLogsOptions{ShowStdout: true, ShowStderr: true}
	)
	c := &cobra.Command{
		Use: "logs", Short: "Display logs for the running containers",
		RunE: func(cmd *cobra.Command, args []string) error {
			conf.interpolate()
			dk, err := conf.docker(args...)
			if err != nil {
				return err
			}
			instmap := conf.HostInstances(Filters(args)...)
			var wg sync.WaitGroup
			err = dk.Run(func(host string, c *client.Client) error {
				instances := instmap[host]
				for _, in := range instances {
					r, err := c.ContainerLogs(cmd.Context(), in.Name, flags)
					if err != nil {
						return err
					}
					wg.Add(1)
					go func(r io.ReadCloser) {
						defer r.Close()
						defer wg.Done()
						reader := &ctxReader{ctx: cmd.Context(), Reader: r}
						_, err = io.Copy(os.Stdout, reader)
						switch err {
						case nil, io.EOF, context.Canceled:
							return
						default:
							log.Println(err)
							// continue
						}
					}(r)
				}
				return nil
			})
			wg.Wait()
			return err
		},
	}
	c.Flags().BoolVarP(&flags.Follow, "follow", "f", flags.Follow, "follow the container logs")
	c.Flags().StringVar(&flags.Since, "since", flags.Since, "")
	return c
}

type ctxReader struct {
	io.Reader
	ctx context.Context
}

func (r *ctxReader) Read(p []byte) (n int, err error) {
	err = r.ctx.Err()
	if err != nil {
		return 0, err
	}
	return r.Reader.Read(p)
}

func hosts(conf *Config) []string {
	m := make(map[string]struct{})
	for _, h := range conf.Hosts {
		if _, ok := m[h.Host]; !ok {
			m[h.Host] = struct{}{}
		}
	}
	hosts := make([]string, 0, len(m))
	for h := range m {
		hosts = append(hosts, h)
	}
	return hosts
}

type volumeData struct {
	io.Reader
	Volume ConfigVolume
}

func uploadToVolume(ctx context.Context, dk *multiDocker, volumes []*volumeData) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	return dk.Run(func(host string, c *client.Client) error {
		for _, vol := range volumes {
			version, err := c.ServerVersion(ctx)
			if err != nil {
				return err
			}
			v, err := c.VolumeCreate(ctx, volume.VolumeCreateBody{
				Name: vol.Volume.Name,
			})
			if err != nil {
				log.Println(err)
				continue
			}
			volume := fmt.Sprintf("%s:/data", v.Name)
			containerCfg := &container.Config{
				Image:   "busybox",
				Volumes: map[string]struct{}{volume: {}},
			}
			container, err := c.ContainerCreate(
				ctx,
				containerCfg,
				&container.HostConfig{
					Binds: []string{volume},
					// AutoRemove: true,
				},
				&network.NetworkingConfig{},
				&specs.Platform{
					Architecture: version.Arch,
					OS:           version.Os,
					OSVersion:    version.KernelVersion,
				},
				"helper",
			)
			if err != nil {
				return err
			}
			defer c.ContainerRemove(ctx, container.ID, types.ContainerRemoveOptions{
				RemoveVolumes: false,
			})
			// content, err := io.ReadAll(vol)
			// if err != nil {
			// 	return err
			// }
			// var buf bytes.Buffer
			// if _, err = buf.Write(content); err != nil {
			// 	return err
			// }

			err = c.CopyToContainer(
				ctx,
				container.ID,
				"/data",
				vol.Reader,
				types.CopyToContainerOptions{},
			)
			if err != nil {
				return err
			}
		}
		return nil
	})
}
