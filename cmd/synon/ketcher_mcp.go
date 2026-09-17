package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"strings"

	"synon-go/internal/ketchermcp"
)

func runKetcherMCPCLI(ctx context.Context, args []string, input io.Reader, output io.Writer) error {
	flags := flag.NewFlagSet("synon-go mcp-ketcher", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	widget := flags.String("widget-gzip", "", "path to the verified Ketcher widget gzip")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: synon-go mcp-ketcher [--widget-gzip <path>]")
	}
	path := strings.TrimSpace(*widget)
	if path == "" {
		path = ketchermcp.DiscoverWidgetPath()
	}
	return ketchermcp.Run(ctx, ketchermcp.Options{Input: input, Output: output, WidgetGzipPath: path})
}
