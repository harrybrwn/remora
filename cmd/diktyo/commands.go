package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"github.com/dgraph-io/ristretto"
	"github.com/harrybrwn/diktyo/web"
	"github.com/sirupsen/logrus"
	"github.com/spf13/cobra"
)

func NewRuntimeCmd(c *web.Crawler) *cobra.Command {
	root := &cobra.Command{
		Use:   "diktyo",
		Short: "Runtime command line for the web crawler",
	}
	root.AddCommand(
		&cobra.Command{
			Use:     "memory",
			Aliases: []string{"mem"},
			RunE: func(cmd *cobra.Command, _ []string) error {
				stats := getMemStats()
				cmd.Printf("heap: %02fMB; sys: %02fMB\nobjects: %v\n",
					toMB(stats.HeapAlloc), toMB(stats.Sys), stats.HeapObjects)
				return nil
			},
		},
		&cobra.Command{
			Use: "count", Aliases: []string{"vertices", "n"},
			Run: func(cmd *cobra.Command, _ []string) { cmd.Printf("count: %d\n", c.N()) },
		},
		&cobra.Command{
			Use: "statistics", Aliases: []string{"stats", "stat"},
			Run: statsCmdRunFunc(c),
		},
		&cobra.Command{
			Use: "clear",
			Run: func(*cobra.Command, []string) { os.Stdout.Write([]byte("\x1b[1J\x1b[1;1H")) },
		},
		&cobra.Command{
			Use: "gc", Short: "Run the garbage collector",
			Run: func(*cobra.Command, []string) { runtime.GC() },
		},
		&cobra.Command{
			Use: "close", Short: "Close one of the spiders",
			Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				return c.CloseSpider(args[0])
			},
		},
		newSetCmd(),
	)
	return root
}

func runtimeCommandHandler(ctx context.Context, stop context.CancelFunc, c *web.Crawler) {
	var (
		sc    = bufio.NewScanner(os.Stdin)
		argch = make(chan []string)
		root  = NewRuntimeCmd(c)
	)
	root.AddCommand(
		&cobra.Command{
			Use: "exit", Aliases: []string{"quit", "q"},
			Run: func(*cobra.Command, []string) { stop() },
		},
	)
	go func() {
		for sc.Scan() {
			argch <- strings.Split(strings.Trim(sc.Text(), "\n\t\r "), " ")
		}
	}()

	fmt.Print("> ")
	for {
		select {
		case <-ctx.Done():
			return
		case args := <-argch:
			root.SetArgs(args)
			err := root.Execute()
			if err != nil {
				fmt.Printf("Error: %v\n", err)
			}
			fmt.Print("> ")
		}
	}
}

const spiderStatFmt = `  - spider %d:
	host:      %s
	fetched:   %d
	waittime:  %v
	queuesize: %d
`

func statsCmdRunFunc(c *web.Crawler) func(*cobra.Command, []string) {
	return func(cmd *cobra.Command, _ []string) {
		out := cmd.OutOrStdout()
		mem := getMemStats()
		sstats := c.SpiderStats()
		fmt.Fprintf(out,
			"statistics:\n  visited: %d\n  queue size: %d\n  spiders count: %d\n",
			c.N(), c.QueueSize(), c.SpiderCount(),
		)
		fmt.Fprintf(out, "  heap: %02fMB\n  sys-mem: %02fMB\n", toMB(mem.HeapAlloc), toMB(mem.Sys))
		var tot, queued int64
		for i, s := range sstats {
			tot += s.PagesFetched
			queued += s.QueueSize
			fmt.Fprintf(out, spiderStatFmt, i, s.Host, s.PagesFetched, s.WaitTime, s.QueueSize)
		}
		fmt.Fprintf(out, "  total fetched: %d\n  total queued: %d\n", tot, queued)
		lsm, vlog := c.DB.Size()
		// m := c.DB.IndexCacheMetrics()
		opts := c.DB.Opts()
		fmt.Fprintf(out, `  badger db:
  dir: %s
  size:
    lsm: %02fMB
    vlog: %02fMB
  max-batch-count: %d
  max-batch-size:  %d
`,
			opts.Dir,
			toMB(uint64(lsm)), toMB(uint64(vlog)),
			c.DB.MaxBatchCount(), c.DB.MaxBatchSize(),
		)
		fmt.Fprintf(out, "  block cache:\n")
		printMetrics(out, c.DB.BlockCacheMetrics())
		fmt.Fprintf(out, "  index cache:\n")
		printMetrics(out, c.DB.IndexCacheMetrics())
	}
}

func printMetrics(out io.Writer, m *ristretto.Metrics) {
	fmt.Fprintf(out, `
    hits:   %d
    misses: %d
    gets: dropped %d, kept     %d
    sets: dropped %d, rejected %d
    cost:
      added:   %d
      evicted: %d
    keys:
      added:   %d
      updated: %d
      evicted: %d
`,
		m.Hits(), m.Misses(),
		m.GetsKept(), m.GetsDropped(),
		m.CostAdded(), m.CostEvicted(),
		m.KeysAdded(), m.KeysUpdated(), m.KeysEvicted(),
		m.SetsDropped(), m.SetsRejected(),
	)
}

func newSetCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "set",
		Short: "Set various settings at runtime",
	}
	c.AddCommand(
		&cobra.Command{
			Use:     "loglevel",
			Aliases: []string{"loglvl", "log-level", "log-lvl"},
			RunE:    setLogLevelRunFunc},
		&cobra.Command{
			Use:  "sleep",
			Args: cobra.ExactArgs(1),
			Run:  func(cmd *cobra.Command, args []string) {},
		},
	)
	return c
}

func runListCmd(cmd *cobra.Command, args []string) error {
	page := web.NewPageFromString(args[0], 0)
	err := page.FetchCtx(cmd.Context())
	if err != nil {
		return err
	}
	for _, l := range page.Links {
		cmd.Println(l)
	}
	cmd.Println(len(page.Links), "links found")
	return nil
}

func runKeywordsCmd(cmd *cobra.Command, args []string) error {
	p := web.NewPageFromString(args[0], 0)
	err := p.FetchCtx(cmd.Context())
	if err != nil {
		return err
	}
	k, err := p.Keywords()
	if err != nil {
		return err
	}
	cmd.Printf("%s\n", bytes.Join(k, []byte{' '}))
	return nil
}

func getMemStats() *runtime.MemStats {
	var s runtime.MemStats
	runtime.ReadMemStats(&s)
	return &s
}

func toMB(bytes uint64) float64 {
	return float64(bytes) / 1024.0 / 1024.0
}

func setLogLevelRunFunc(_ *cobra.Command, args []string) error {
	if len(args) < 1 {
		return errors.New("no level given")
	}
	lvl, err := logrus.ParseLevel(args[0])
	if err != nil {
		return err
	}
	log.SetLevel(lvl)
	return nil
}
