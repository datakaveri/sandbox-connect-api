package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sandbox-backend-service/internal/filesconnect"
	"sandbox-backend-service/internal/output"
	"sandbox-backend-service/internal/outputupload"
	"time"
)

func main() {
	workspace := flag.String("workspace", "/workspace", "scratch directory")
	id := flag.String("output-id", "", "trusted output ID")
	prefix := flag.String("review-prefix", "", "trusted review prefix")
	manifest := flag.String("manifest", "/workspace/manifest.json", "manifest destination")
	csvOnly := flag.Bool("csv-only", true, "require CSV deliverables")
	limits := output.Limits{}
	flag.IntVar(&limits.MaxManifestBytes, "max-manifest-bytes", 262144, "manifest byte limit")
	flag.IntVar(&limits.MaxManifestFiles, "max-files", output.DefaultMaxManifestFiles, "file count limit")
	flag.Int64Var(&limits.MaxFileBytes, "max-file-bytes", output.DefaultMaxFileBytes, "file byte limit")
	flag.Int64Var(&limits.MaxOutputBytes, "max-output-bytes", output.DefaultMaxOutputBytes, "total byte limit")
	flag.Parse()
	if *id == "" || !*csvOnly || flag.NArg() != 0 || limits.Validate() != nil {
		fmt.Fprintln(os.Stderr, "invalid upload arguments")
		os.Exit(1)
	}
	client, err := filesconnect.NewUploadClient(filesconnect.Config{BaseURL: os.Getenv("FILE_SERVICE_BASE_URL"), ServiceToken: os.Getenv("FILE_SERVICE_TOKEN"), Timeout: time.Minute, Limits: limits})
	if err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
		defer cancel()
		err = (outputupload.Uploader{Service: client, Limits: limits}).Run(ctx, *workspace, *id, *prefix, *manifest)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
