package main

import (
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/harrybrwn/env"
	"github.com/harrybrwn/x/cobrautil"
	"github.com/spf13/cobra"
)

func main() {
	root := NewRootCmd()
	err := root.Execute()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

type Config struct {
	Host string
	Port int
}

func NewRootCmd() *cobra.Command {
	var config Config
	c := cobra.Command{
		Use:           "remoractl",
		Short:         "Control remora crawler APIs",
		SilenceErrors: true,
		SilenceUsage:  true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			err := env.ReadEnv(&config)
			if err != nil {
				return err
			}
			if len(config.Host) == 0 {
				config.Host = "localhost"
			}
			if config.Port == 0 {
				config.Port = 3010
			}
			return nil
		},
	}
	c.AddCommand(
		NewListCmd(),
		NewStatusCmd(&config),
		NewCrawlCmd(&config),
		NewConfigCmd(&config),
	)
	c.SetUsageTemplate(cobrautil.IndentedCobraHelpTemplate)
	return &c
}

func NewListCmd() *cobra.Command {
	c := cobra.Command{
		Use:     "list",
		Short:   "list stuff",
		Aliases: []string{"ls"},
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("not implemented yet")
			return nil
		},
	}
	return &c
}

func NewStatusCmd(cfg *Config) *cobra.Command {
	c := cobra.Command{
		Use: "status",
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := http.Get(fmt.Sprintf("http://%s:%d/status", cfg.Host, cfg.Port))
			if err != nil {
				return err
			}
			defer res.Body.Close()
			io.Copy(os.Stdout, res.Body)
			os.Stdout.Write([]byte{'\n'})
			return nil
		},
	}
	return &c
}

func NewCrawlCmd(cfg *Config) *cobra.Command {
	c := cobra.Command{
		Use: "crawl",
		RunE: func(cmd *cobra.Command, args []string) error {
			return nil
		},
	}
	return &c
}

func NewConfigCmd(cfg *Config) *cobra.Command {
	c := cobra.Command{
		Use: "config",
		RunE: func(cmd *cobra.Command, args []string) error {
			res, err := http.Get(fmt.Sprintf("http://%s:%d/config", cfg.Host, cfg.Port))
			if err != nil {
				return err
			}
			defer res.Body.Close()
			io.Copy(os.Stdout, res.Body)
			os.Stdout.Write([]byte{'\n'})
			return nil
		},
	}
	return &c
}
