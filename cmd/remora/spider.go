package main

import (
	"github.com/harrybrwn/diktyo/cmd"
	"github.com/spf13/cobra"
)

func newSpiderCmd(conf *cmd.Config) *cobra.Command {
	c := &cobra.Command{
		Use: "spider", Short: "",
	}
	return c
}
