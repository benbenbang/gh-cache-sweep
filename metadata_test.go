package main

import (
	"bytes"
	"context"
	"fmt"
	"testing"
)

func TestVersionInfo(t *testing.T) {
	oldVersion, oldBuildTime, oldURL := Version, BuildTime, ProjectUrl
	t.Cleanup(func() {
		Version, BuildTime, ProjectUrl = oldVersion, oldBuildTime, oldURL
	})
	Version, BuildTime, ProjectUrl = "v1.2.3", "2026-09-10T12:34:56Z", "https://github.com/example/gh-cache-sweep"
	want := "gh-cache-sweep v1.2.3\nBuild time: 2026-09-10T12:34:56Z\nRepository: https://github.com/example/gh-cache-sweep\n"
	if got := versionInfo(); got != want {
		t.Fatalf("versionInfo() = %q; want %q", got, want)
	}
}

func TestSweepVersionDoesNotCallGH(t *testing.T) {
	for _, args := range [][]string{
		{"--version"},
		{"--version", "--org", "acme", "--yes"},
	} {
		t.Run(fmt.Sprint(args), func(t *testing.T) {
			var out, errOut bytes.Buffer
			if err := sweep(context.Background(), args, scriptedRunner(t), &out, &errOut); err != nil {
				t.Fatal(err)
			}
			if got := out.String(); got != versionInfo() {
				t.Errorf("stdout = %q; want %q", got, versionInfo())
			}
			if errOut.Len() != 0 {
				t.Errorf("unexpected stderr: %s", &errOut)
			}
		})
	}
}
