package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sandbox-backend-service/internal/outputruntime"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "stage is required")
		os.Exit(1)
	}
	stage := os.Args[1]
	flags := flag.NewFlagSet(stage, flag.ExitOnError)
	workspace := flags.String("workspace", "/workspace", "scratch directory")
	source := flags.String("source", "", "source notebook")
	outputID := flags.String("output-id", "", "trusted output ID")
	mapPath := flags.String("map", "", "platform replacement map")
	outputDir := flags.String("output", "/workspace/output", "output directory")
	timeout := flags.Duration("timeout", 20*time.Minute, "stage timeout")
	streamLogs := flags.Bool("stream-logs", false, "copy bounded command output to container stdout")
	maxLogBytes := flags.Int64("max-log-bytes", outputruntime.MaxLogBytes, "maximum retained and streamed command output bytes")
	flags.Parse(os.Args[2:])
	if *timeout <= 0 || *maxLogBytes <= 0 || *maxLogBytes > outputruntime.MaxAllowedLogBytes || flags.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "invalid stage arguments")
		os.Exit(1)
	}
	absolute, err := filepath.Abs(*workspace)
	if err != nil {
		os.Exit(1)
	}
	runner := outputruntime.Runner{Workspace: absolute, MaxLogBytes: *maxLogBytes}
	if *streamLogs {
		runner.StreamOutput = os.Stdout
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	switch stage {
	case "prepare":
		err = runner.Prepare(*source, *outputID)
	case "convert":
		err = runner.Convert(ctx)
	case "configure":
		err = runner.Configure(*mapPath)
	case "execute":
		err = runner.Execute(ctx, *outputDir)
	default:
		err = fmt.Errorf("unknown stage")
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
