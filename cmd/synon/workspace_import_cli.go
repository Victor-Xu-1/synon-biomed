package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"synon-go/internal/workspaceimport"
)

func runWorkspaceImportCLI(ctx context.Context, args []string, output io.Writer) error {
	if output == nil {
		return errors.New("workspace import output is required")
	}
	if len(args) == 0 || (args[0] != "inspect" && args[0] != "plan") {
		return errors.New("usage: Synon Biomed workspace-import inspect|plan --source <path> [--target-home <path>] [--include-paths]")
	}
	mode := args[0]
	flags := flag.NewFlagSet("workspace-import "+mode, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	source := flags.String("source", "", "source database or data root")
	targetHome := flags.String("target-home", "", "existing target data root used only for capacity planning")
	includePaths := flags.Bool("include-paths", false, "include local filesystem paths in JSON output")
	if err := flags.Parse(args[1:]); err != nil {
		return fmt.Errorf("parse workspace import %s options: %w", mode, err)
	}
	if flags.NArg() != 0 || strings.TrimSpace(*source) == "" {
		return errors.New("workspace import --source is required and positional arguments are not accepted")
	}
	if mode == "plan" && strings.TrimSpace(*targetHome) == "" {
		return errors.New("workspace import plan requires --target-home")
	}
	plan, err := workspaceimport.Inspect(ctx, workspaceimport.Options{
		SourcePath: *source, TargetHome: *targetHome, IncludePaths: *includePaths,
	})
	if err != nil {
		if !*includePaths {
			return errors.New("inspect workspace import source failed")
		}
		return fmt.Errorf("inspect workspace import source: %w", err)
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(plan); err != nil {
		return fmt.Errorf("write workspace import plan: %w", err)
	}
	if mode == "plan" && !plan.ReadyForStagedApply {
		return errors.New("workspace import plan is not ready for staged apply")
	}
	return nil
}
