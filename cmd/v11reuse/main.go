package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"synon-go/internal/compat/v11reuse"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout io.Writer, stderr io.Writer) int {
	flags := flag.NewFlagSet("v11reuse", flag.ContinueOnError)
	flags.SetOutput(stderr)
	source := flags.String("source", "", "read-only SynonBiomed v1.1 source directory")
	target := flags.String("target", "", "reuse manifest JSON output")
	reviewTarget := flags.String("review-target", "", "optional generated Markdown review output")
	check := flags.Bool("check", false, "verify existing outputs instead of writing them")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if strings.TrimSpace(*source) == "" || strings.TrimSpace(*target) == "" {
		fmt.Fprintln(stderr, "v11reuse: --source and --target are required")
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(stderr, "v11reuse: unexpected positional arguments: %s\n", strings.Join(flags.Args(), " "))
		return 2
	}

	rules, err := v11reuse.BaselineRules(*source)
	if err != nil {
		fmt.Fprintf(stderr, "v11reuse: build baseline rules: %v\n", err)
		return 1
	}
	manifest, err := v11reuse.Build(*source, rules)
	if err != nil {
		fmt.Fprintf(stderr, "v11reuse: inventory baseline: %v\n", err)
		return 1
	}
	manifest.SourceCommit = detectSourceCommit(*source)

	manifestJSON, err := v11reuse.Marshal(manifest)
	if err != nil {
		fmt.Fprintf(stderr, "v11reuse: %v\n", err)
		return 1
	}
	if err := v11reuse.WriteOutput(*target, manifestJSON, *check); err != nil {
		fmt.Fprintf(stderr, "v11reuse: write manifest: %v\n", err)
		return 1
	}
	if strings.TrimSpace(*reviewTarget) != "" {
		if err := v11reuse.WriteOutput(*reviewTarget, v11reuse.ReviewMarkdown(manifest), *check); err != nil {
			fmt.Fprintf(stderr, "v11reuse: write review: %v\n", err)
			return 1
		}
	}

	mode := "generated"
	if *check {
		mode = "verified"
	}
	fmt.Fprintf(
		stdout,
		"v11reuse: %s entries=%d files=%d symlinks=%d bytes=%d agents=%d skills=%d tools=%d mcp=%d tree=%s\n",
		mode,
		manifest.Summary.Entries,
		manifest.Summary.Files,
		manifest.Summary.Symlinks,
		manifest.Summary.TotalBytes,
		manifest.Summary.ByCategory["agent"],
		manifest.Summary.ByCategory["skill"],
		manifest.Summary.ByCategory["tool"],
		manifest.Summary.ByCategory["mcp-server"],
		manifest.SourceTreeSHA256,
	)
	return 0
}

func detectSourceCommit(source string) string {
	command := exec.Command("git", "-C", source, "rev-parse", "HEAD")
	output, err := command.Output()
	if err != nil {
		return ""
	}
	commit := strings.TrimSpace(string(output))
	if len(commit) != 40 {
		return ""
	}
	for _, character := range commit {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return ""
			}
		}
	}
	return commit
}
