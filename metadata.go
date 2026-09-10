package main

import "fmt"

// These defaults are overridden at build time using go build -ldflags -X.
var (
	Version    = "dev"
	BuildTime  = "unknown"
	ProjectUrl = "https://github.com/benbenbang/gh-cache-sweep"
)

func versionInfo() string {
	return fmt.Sprintf("gh-cache-sweep %s\nBuild time: %s\nRepository: %s\n", Version, BuildTime, ProjectUrl)
}
