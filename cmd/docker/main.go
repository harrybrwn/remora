package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"sync"

	flag "github.com/spf13/pflag"
)

func main() {
	var (
		wg        sync.WaitGroup
		stream    bool
		hostnames []string
		out       io.ReadWriter = nil
		osArgs                  = os.Args[:]
	)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	flag := flag.NewFlagSet("docker-flags", flag.ExitOnError)
	flag.StringArrayVarP(&hostnames, "host", "H", hostnames, "hosts to send the docker command to")
	flag.BoolVarP(&stream, "stream-out", "s", stream, "stream the command output instead of collecting it and printing it in order")
	flag.Parse(os.Args[1:])

	if !arrContains(osArgs, "--") {
		log.Fatal("no command given after \"--\"")
	}

	args := flag.Args()
	if len(hostnames) == 0 {
		log.Fatal("no hosts")
	}

	if stream {
		out = os.Stdout
	}

	hosts := newHosts(&wg, out, hostnames...)

	wg.Add(len(hosts))
	for _, c := range hosts {
		go c.run(ctx, args)
	}
	wg.Wait()

	if !stream {
		for _, c := range hosts {
			fmt.Println(c.host)
			io.Copy(os.Stdout, c.out)
			fmt.Println()
		}
	}
}

func newHosts(wg *sync.WaitGroup, out io.ReadWriter, hostnames ...string) []*Host {
	hosts := make([]*Host, len(hostnames))
	for i, h := range hostnames {
		hosts[i] = newHost(wg, out, h)
	}
	return hosts
}

func newHost(wg *sync.WaitGroup, out io.ReadWriter, host string) *Host {
	if out == nil {
		out = new(bytes.Buffer)
	}
	return &Host{wg: wg, host: host, out: out}
}

type Host struct {
	wg   *sync.WaitGroup
	host string
	out  io.ReadWriter
}

func (h *Host) run(ctx context.Context, args []string) error {
	defer h.wg.Done()
	args = append([]string{"-H", h.host}, args...)
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout, cmd.Stderr = h.out, h.out
	return cmd.Run()
}

func splitCommands(args []string) [][]string {
	return nil
}

func arrContains(arr []string, s string) bool {
	for _, a := range arr {
		if a == s {
			return true
		}
	}
	return false
}
