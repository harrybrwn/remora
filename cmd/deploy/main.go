package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"math/rand"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/client"
	"github.com/fatih/color"
	"github.com/harrybrwn/config"
	"github.com/pkg/errors"
	"github.com/spf13/cobra"
)

func main() {
	var (
		err  error
		conf Config
		ctx  = context.Background()
	)
	ctx, cancel := signal.NotifyContext(ctx, os.Interrupt)
	defer cancel()

	cli := NewCliRoot(&conf)
	err = cli.ExecuteContext(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
	}
}

func NewCliRoot(conf *Config) *cobra.Command {
	var (
		dk         = &docker{wg: new(sync.WaitGroup)}
		test       = false
		configfile = "./deployment.yml"
	)
	c := &cobra.Command{
		Use:           "deploy",
		Short:         "Container deployment tool for the web crawler",
		SilenceErrors: true,
		SilenceUsage:  true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			dir, file := filepath.Split(configfile)
			if dir == "" {
				dir = "."
			}
			config.AddPath(dir)
			config.AddFile(file)
			config.SetType("yaml")
			config.SetConfig(conf)
			err := config.ReadConfigFile()
			if err != nil {
				return err
			}
			conf.interpolate()
			return nil
		},
	}
	c.PersistentFlags().StringVarP(
		&configfile, "config", "c",
		configfile, "use a different config file")
	c.PersistentFlags().BoolVar(&test, "test", test, "")
	c.PersistentFlags().MarkHidden("test")

	var listOpts types.ContainerListOptions
	ps := &cobra.Command{
		Use: "ps", Short: "Show all running containers",
		RunE: func(cmd *cobra.Command, args []string) error {
			if test {
				docker := newMultiDocker(conf.HostConfigs())
				tab := tabwriter.NewWriter(
					os.Stdout, 1, 4, 3, ' ', tabwriter.StripEscape,
				)
				err := docker.Run(
					cmd.Context(),
					multiPSFunc(conf, tab, listOpts),
				)
				tab.Flush()
				return err
			}
			commands, err := ps(conf.instances(args...), dk, listOpts.All)
			return chain(err, run(cmd.Context(), dk, commands))
		},
	}
	ps.Flags().BoolVarP(&listOpts.All, "all", "a", false, "")

	c.AddCommand(
		ps,
		newLogsCmd(conf),
		newListCmd(conf),
		newUpCmd(conf),
		newUploadCmd(conf),
		newInstancesCmd(conf, "down", "Stop and remove all containers", down),
		newInstancesCmd(conf, "stop", "Stop all the containers", stop),
		&cobra.Command{
			Use: "restart", Short: "Restart all the running containers",
			RunE: func(cmd *cobra.Command, args []string) error {
				commands, err := restart(conf.instances(args...), dk)
				return chain(err, run(cmd.Context(), dk, commands))
			},
		},
		&cobra.Command{
			Use: "build", Short: "Build all the images the deployment depends on",
			RunE: func(cmd *cobra.Command, args []string) error {
				commands, err := build(conf.builds(args...), dk)
				return chain(err, run(cmd.Context(), dk, commands))
			},
		},
		&cobra.Command{
			Use: "pull", Short: "Pull images to all hosts",
			RunE: func(cmd *cobra.Command, args []string) error {
				commands, err := pull(conf.builds(args...), dk)
				return chain(err, run(cmd.Context(), dk, commands))
			},
		},
	)
	c.SetOut(os.Stdout)
	c.SetErr(os.Stderr)
	return c
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

func chain(e1, e2 error) error {
	if e1 != nil {
		return e1
	}
	return e2
}

func newInstancesCmd(conf *Config, use, short string, fn func([]Instance, *docker) ([]Command, error)) *cobra.Command {
	var (
		dk = &docker{wg: new(sync.WaitGroup)}
	)
	return &cobra.Command{
		Use: use, Short: short,
		RunE: func(cmd *cobra.Command, args []string) error {
			commands, err := fn(conf.instances(args...), dk)
			return chain(err, run(cmd.Context(), dk, commands))
		},
	}
}

func newUpCmd(conf *Config) *cobra.Command {
	var (
		pull        bool
		interactive bool
		term        bool
	)
	dk := &docker{wg: new(sync.WaitGroup)}
	c := &cobra.Command{
		Use: "up [<name:host>...]", Short: "Run all the containers",
		RunE: func(cmd *cobra.Command, args []string) error {
			commands, err := up(conf.instances(args...), dk, pull, interactive, term)
			return chain(err, run(cmd.Context(), dk, commands))
		},
	}
	flags := c.Flags()
	flags.BoolVar(&pull, "pull", pull, "pull the image before running")
	flags.BoolVarP(&interactive, "interactive", "i", interactive, "run container interactively")
	flags.BoolVarP(&term, "term", "t", term, "run the container as a terminal")
	return c
}

func newLogsCmd(conf *Config) *cobra.Command {
	var opts = types.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
	}
	c := cobra.Command{
		Use: "logs", Short: "Display container logging",
		RunE: func(cmd *cobra.Command, args []string) error {
			docker := newMultiDocker(conf.HostConfigs(Filters(args)...))
			return containerLogs(cmd.Context(), docker, conf.instances(args...), opts)
		},
	}
	flags := c.Flags()
	flags.BoolVarP(&opts.Follow, "follow", "f", opts.Follow, "follow the logs")
	flags.StringVar(&opts.Since, "since", opts.Since, "Show logs since timestamp (e.g. 2013-01-02T13:23:37Z) or relative (e.g. 42m for 42 minutes)")
	return &c
}

func newListCmd(conf *Config) *cobra.Command {
	var (
		notrunc bool
		trunc   = trunc
	)
	cmd := &cobra.Command{
		Use: "list", Short: "List all containers and builds",
		Aliases: []string{"ls"},
		RunE: func(cmd *cobra.Command, args []string) error {
			tab := tabwriter.NewWriter(os.Stdout, 1, 4, 3, ' ', tabwriter.StripEscape)
			instances := conf.instances(args...)
			builds := conf.builds(args...)
			if notrunc {
				trunc = func(s string, _ int) string { return s }
			}
			fmt.Println("Instances:")
			row(tab, "", "HOST", "IMAGE", "NAME", "COMMAND", "VOLUMES")
			for _, in := range instances {
				row(tab,
					"",
					in.Host,
					in.Image,
					in.Name,
					trunc(strings.Join(in.Command, " "), 35),
					"["+fmt.Sprintf("%q", strings.Join(in.Volumes, ", "))+"]",
				)
			}
			tab.Flush()
			fmt.Println("Builds:")
			row(tab, "", "HOST", "IMAGE", "DOCKERFILE", "CONTEXT")
			for _, b := range builds {
				row(tab, "", b.Host, b.Image, b.Dockerfile, b.Context)
			}
			tab.Flush()
			fmt.Printf("%d instances, %d builds\n", len(instances), len(builds))
			return nil
		},
	}
	cmd.Flags().BoolVar(&notrunc, "no-trunc", notrunc, "disable string truncation")
	return cmd
}

func trunc(s string, max int) string {
	if len(s) > max {
		return fmt.Sprintf("%s...", s[:max])
	}
	return s
}

func row(tab *tabwriter.Writer, row ...string) error {
	_, err := tab.Write([]byte(strings.Join(row, "\t") + "\n"))
	return err
}

func newUploadCmd(conf *Config) *cobra.Command {
	var (
		volume  string
		filters []string
	)
	c := cobra.Command{
		Use: "upload", Short: "Upload a file to a volume on all hosts",
		Long: "Upload a file to a volume on all hosts\n\n" +
			"This command will use the configuration file to upload any number of\n" +
			"files to a remote docker container.",
		RunE: func(cmd *cobra.Command, args []string) error {
			dk := conf.docker(filters...)
			if len(args) == 0 && volume == "" {
				for _, up := range conf.Uploads {
					args = append(args, up.Files...)
					volume = up.Volume
				}
			}
			if volume == "" {
				return errors.New("no volume name")
			}
			if len(args) == 0 {
				return errors.New("no files to upload")
			}

			return dk.Run(cmd.Context(), func(ctx context.Context, conf *HostConfig, cli *client.Client) error {
				files := make([]fs.File, 0, len(args))
				for _, filename := range args {
					cmd.Printf("uploading %q to %s \"%s:%[1]s\"\n", filename, conf.Host, volume)
					f, err := os.OpenFile(filename, os.O_RDONLY, os.ModePerm)
					if err != nil {
						return err
					}
					stat, err := f.Stat()
					if err != nil {
						f.Close()
						return err
					}
					var buf bytes.Buffer
					if _, err = buf.ReadFrom(f); err != nil {
						f.Close()
						return err
					}
					f.Close()
					files = append(files, &file{
						r:    &buf,
						stat: stat,
					})
				}
				defer cmd.Printf("done uploading to %s\n", conf.Host)
				return uploadFilesToVolume(ctx, cli, volume, files)
			})
		},
	}
	flags := c.Flags()
	flags.StringVarP(&volume, "volume", "v", volume, "volume name")
	flags.StringArrayVarP(&filters, "filter", "f", filters,
		"filter out by <host:name> pairs")
	return &c
}

type file struct {
	r    io.Reader
	stat os.FileInfo
}

func (f *file) Read(b []byte) (int, error) {
	return f.r.Read(b)
}
func (f *file) Stat() (fs.FileInfo, error) { return f.stat, nil }
func (f *file) Close() error               { return nil }

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

func up(inst []Instance, dk *docker, pull bool, i, t bool) (commands []Command, err error) {
	if len(inst) == 0 {
		err = errors.New("no instances to deploy")
		return
	}
	for _, in := range inst {
		c := dk.Cmd().WithHost(in.Host).ContainerRun(
			in.Image, in.Command,
		).Hostname(
			fmt.Sprintf("%s-%s", in.Host, in.Name),
		)
		if i {
			c = c.Interactive()
		}
		if t {
			c = c.Term()
		}
		if pull {
			c = c.Pull("always")
		}
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

func restart(inst []Instance, dk *docker) (commands []Command, err error) {
	for _, in := range inst {
		cmd := dk.Cmd().WithHost(in.Host).ContainerRestart(in.Name)
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

func multiPSFunc(conf *Config, tab *tabwriter.Writer, opts types.ContainerListOptions) runFunc {
	var mu sync.Mutex
	row(tab, "HOST", "CONTAINER ID", "IMAGE", "COMMAND", "CREATED", "STATUS", "PORTS", "NAMES")
	return func(ctx context.Context, conf *HostConfig, cli *client.Client) error {
		containers, err := cli.ContainerList(ctx, opts)
		if err != nil {
			return err
		}
		if len(containers) == 0 {
			return nil
		}
		for _, container := range containers {
			mu.Lock()
			row(tab,
				conf.Host,
				container.ID[:12],
				container.Image,
				trunc(container.Command, 30),
				time.Since(time.Unix(container.Created, 0)).String(),
				container.Status,
				fmt.Sprintf("%v", container.Ports),
				strings.Join(container.Names, ", "),
			)
			mu.Unlock()
		}
		return err
	}
}

func pull(builds []Build, dk *docker) (commands []Command, err error) {
	for _, b := range builds {
		cmd := dk.Cmd().WithHost(b.Host).ImagePull(b.Image)
		commands = append(commands, cmd)
	}
	return
}

var (
	fg = [...]color.Attribute{
		color.FgRed,
		color.FgGreen,
		color.FgYellow,
		color.FgBlue,
		color.FgMagenta,
		color.FgCyan,
		color.FgWhite,
		color.FgHiRed,
		color.FgHiGreen,
		color.FgHiYellow,
		color.FgHiBlue,
		color.FgHiMagenta,
		color.FgHiCyan,
	}
)

func newcolorset() *colorset {
	return &colorset{
		s: make(map[attrpair]struct{}),
	}
}

type attrpair struct {
	a, b color.Attribute
}

type colorset struct {
	mu sync.Mutex
	s  map[attrpair]struct{}
}

func (c *colorset) New() *color.Color {
	c.mu.Lock()
	defer c.mu.Unlock()
	l := len(fg)
	for i := 0; i < 20; i++ {
		attr := fg[rand.Intn(l)]
		pair := attrpair{
			a: attr,
		}
		bold := rand.Intn(2) == 0
		if bold {
			pair.b = color.Bold
		} else {
			pair.b = color.Reset
		}
		_, ok := c.s[pair]
		if !ok {
			c.s[pair] = struct{}{}
			if bold {
				return color.New(pair.a, pair.b)
			}
			return color.New(pair.a)
		}
	}
	return color.New(fg[rand.Intn(l)])
}
