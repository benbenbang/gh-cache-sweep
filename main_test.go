package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"reflect"
	"strings"
	"testing"
	"time"
)

// A script rejects every unexpected command, including any extra deletion.
type runnerStep struct {
	args   []string
	output string
	err    error
	action func(context.Context) ([]byte, error)
}

func scriptedRunner(t *testing.T, steps ...runnerStep) runner {
	t.Helper()
	next := 0
	t.Cleanup(func() {
		if next != len(steps) {
			t.Errorf("consumed %d of %d runner steps", next, len(steps))
		}
	})
	return func(ctx context.Context, args ...string) ([]byte, error) {
		t.Helper()
		if next >= len(steps) {
			t.Fatalf("unexpected command: %q", args)
		}
		step := steps[next]
		next++
		if !reflect.DeepEqual(args, step.args) {
			t.Fatalf("command %d = %q; want %q", next, args, step.args)
		}
		if _, ok := ctx.Deadline(); !ok {
			t.Error("runner context has no deadline")
		}
		if step.action != nil {
			return step.action(ctx)
		}
		return []byte(step.output), step.err
	}
}

func apiStep(endpoint string, paginate bool, output string) runnerStep {
	args := []string{"api", "--hostname", "github.com", "--method", "GET", endpoint}
	if paginate {
		args = append(args, "--paginate")
	}
	return runnerStep{args: args, output: output}
}

func supportStep() runnerStep {
	return runnerStep{args: []string{"cache", "delete", "--help"}, output: "--all --succeed-on-no-caches"}
}

func deleteStep(repo string) runnerStep {
	return runnerStep{args: []string{"cache", "delete", "--all", "--succeed-on-no-caches", "--repo", "github.com/" + repo}}
}

func requireContains(t *testing.T, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Errorf("output %q does not contain %q", got, want)
	}
}

func TestSweepApproval(t *testing.T) {
	t.Setenv("GH_HOST", "")
	for _, tc := range []struct {
		name    string
		flags   []string
		execute bool
	}{
		{name: "default preview"},
		{name: "approved", flags: []string{"--yes"}, execute: true},
		{name: "dry run", flags: []string{"--dry-run"}},
		{name: "dry run overrides yes", flags: []string{"--yes", "--dry-run"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			steps := []runnerStep{apiStep("repos/acme/tool", false, `{"full_name":"acme/tool"}`)}
			if tc.execute {
				// Empty command output models successful deletion with no caches present.
				steps = append(steps, supportStep(), deleteStep("acme/tool"))
			}
			var out, errOut bytes.Buffer
			err := sweep(context.Background(), append([]string{"--repo", "acme/tool"}, tc.flags...), scriptedRunner(t, steps...), &out, &errOut)
			if err != nil {
				t.Fatal(err)
			}
			want := "Selected 1 repositories on github.com.\n"
			if tc.execute {
				want += "Deleting Actions caches: acme/tool\nSummary: selected=1 succeeded=1 failed=0 unattempted=0\n"
			} else {
				want += "Would delete all Actions caches: acme/tool\nPreview only: no caches deleted. Pass --yes without --dry-run to delete.\n"
			}
			if out.String() != want || errOut.Len() != 0 {
				t.Fatalf("stdout=%q stderr=%q; want stdout=%q, empty stderr", out.String(), errOut.String(), want)
			}
		})
	}
}

func TestDiscoveryScopes(t *testing.T) {
	t.Setenv("GH_HOST", "")
	const owned = `[{"full_name":"alice/private"},{"full_name":"bob/private"},{"full_name":"ALICE/other"},{"full_name":"bobby/not-bob"}]`
	for _, tc := range []struct {
		name  string
		args  []string
		steps []runnerStep
		want  []repository
	}{
		{"org", []string{"--org", "acme"}, []runnerStep{apiStep("orgs/acme/repos?type=all&per_page=100", true, `[{"full_name":"acme/private"}]`)}, []repository{{FullName: "acme/private"}}},
		{"team", []string{"--team", "acme/core"}, []runnerStep{apiStep("orgs/acme/teams/core/repos?per_page=100", true, `[{"full_name":"acme/tool"}]`)}, []repository{{FullName: "acme/tool"}}},
		{"repo", []string{"--repo", "acme/tool"}, []runnerStep{apiStep("repos/acme/tool", false, `{"full_name":"acme/tool"}`)}, []repository{{FullName: "acme/tool"}}},
		{"self", []string{"--user", "@me"}, []runnerStep{apiStep("user", false, `{"login":"alice"}`), apiStep("user/repos?affiliation=owner,collaborator&visibility=all&per_page=100", true, owned)}, []repository{{FullName: "ALICE/other"}, {FullName: "alice/private"}}},
		{"other user", []string{"--user", "bob"}, []runnerStep{apiStep("user", false, `{"login":"alice"}`), apiStep("user/repos?affiliation=owner,collaborator&visibility=all&per_page=100", true, owned)}, []repository{{FullName: "bob/private"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o, err := parseOptions(tc.args, &bytes.Buffer{})
			if err != nil {
				t.Fatal(err)
			}
			got, err := discover(context.Background(), scriptedRunner(t, tc.steps...), o)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("repositories=%+v; want %+v", got, tc.want)
			}
		})
	}
}

func TestSweepPaginatedDeduplicatedSorted(t *testing.T) {
	t.Setenv("GH_HOST", "")
	pages := `[{"full_name":"acme/z"},{"full_name":"acme/b"}]
[][{"full_name":"ACME/B"},{"full_name":"acme/a","archived":true}]
[{"full_name":"acme/c"}]`
	for _, skip := range []bool{false, true} {
		name := "include archived"
		if skip {
			name = "skip archived"
		}
		t.Run(name, func(t *testing.T) {
			args := []string{"--org", "acme", "--yes"}
			names := []string{"acme/a", "acme/b", "acme/c", "acme/z"}
			if skip {
				args = append(args, "--skip-archived")
				names = names[1:]
			}
			steps := []runnerStep{apiStep("orgs/acme/repos?type=all&per_page=100", true, pages), supportStep()}
			for _, name := range names {
				steps = append(steps, deleteStep(name))
			}
			var out, errOut bytes.Buffer
			if err := sweep(context.Background(), args, scriptedRunner(t, steps...), &out, &errOut); err != nil {
				t.Fatal(err)
			}
			if errOut.Len() != 0 {
				t.Fatalf("stderr=%q", errOut.String())
			}
		})
	}
}

func TestSweepEmptySelection(t *testing.T) {
	t.Setenv("GH_HOST", "")
	for _, data := range []string{`[][]`, `[{"full_name":"acme/old","archived":true}]`} {
		t.Run(data, func(t *testing.T) {
			var out, errOut bytes.Buffer
			err := sweep(context.Background(), []string{"--org", "acme", "--yes", "--skip-archived"}, scriptedRunner(t, apiStep("orgs/acme/repos?type=all&per_page=100", true, data)), &out, &errOut)
			if err != nil {
				t.Fatal(err)
			}
			if out.String() != "Selected 0 repositories on github.com.\n" || errOut.Len() != 0 {
				t.Fatalf("stdout=%q stderr=%q", out.String(), errOut.String())
			}
		})
	}
}

func TestSweepFailureContinues(t *testing.T) {
	t.Setenv("GH_HOST", "")
	failure := deleteStep("acme/b")
	failure.err = errors.New("permission denied")
	var out, errOut bytes.Buffer
	err := sweep(context.Background(), []string{"--org", "acme", "--yes"}, scriptedRunner(t,
		apiStep("orgs/acme/repos?type=all&per_page=100", true, `[{"full_name":"acme/c"},{"full_name":"acme/b"},{"full_name":"acme/a"}]`),
		supportStep(), deleteStep("acme/a"), failure, deleteStep("acme/c")), &out, &errOut)
	if err == nil {
		t.Fatal("expected non-nil error for main's nonzero exit path")
	}
	requireContains(t, err.Error(), "cache cleanup failed for 1 repositories")
	requireContains(t, out.String(), "Summary: selected=3 succeeded=2 failed=1 unattempted=0\n")
	requireContains(t, errOut.String(), "FAILED acme/b: permission denied\n")
}

func TestSweepDiscoveryFailsClosed(t *testing.T) {
	t.Setenv("GH_HOST", "")
	partialErr := errors.New("later page failed")
	partial := apiStep("orgs/acme/repos?type=all&per_page=100", true, `[{"full_name":"acme/first"}]`)
	partial.err = partialErr
	for _, tc := range []struct {
		name  string
		args  []string
		steps []runnerStep
		cause error
	}{
		{"partial output with error", []string{"--org", "acme"}, []runnerStep{partial}, partialErr},
		{"malformed later page", []string{"--org", "acme"}, []runnerStep{apiStep("orgs/acme/repos?type=all&per_page=100", true, `[{"full_name":"acme/first"}][{"full_name":`)}, nil},
		{"trailing garbage", []string{"--org", "acme"}, []runnerStep{apiStep("orgs/acme/repos?type=all&per_page=100", true, `[{"full_name":"acme/first"}] garbage`)}, nil},
		{"wrong page type", []string{"--org", "acme"}, []runnerStep{apiStep("orgs/acme/repos?type=all&per_page=100", true, `{}`)}, nil},
		{"invalid repository on later page", []string{"--org", "acme"}, []runnerStep{apiStep("orgs/acme/repos?type=all&per_page=100", true, `[{"full_name":"acme/first"}][{"full_name":"../bad"}]`)}, nil},
		{"malformed repo", []string{"--repo", "acme/tool"}, []runnerStep{apiStep("repos/acme/tool", false, `{`)}, nil},
		{"missing repo name", []string{"--repo", "acme/tool"}, []runnerStep{apiStep("repos/acme/tool", false, `{}`)}, nil},
		{"malformed viewer", []string{"--user", "@me"}, []runnerStep{apiStep("user", false, `{`)}, nil},
		{"missing viewer login", []string{"--user", "@me"}, []runnerStep{apiStep("user", false, `{}`)}, nil},
		{"viewer request failure", []string{"--user", "@me"}, []runnerStep{{args: apiStep("user", false, "").args, output: `{"login":"alice"}`, err: partialErr}}, partialErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			err := sweep(context.Background(), append(tc.args, "--yes"), scriptedRunner(t, tc.steps...), &out, &errOut)
			if err == nil {
				t.Fatal("expected discovery error")
			}
			requireContains(t, err.Error(), "repository discovery failed (no deletion attempted)")
			if tc.cause != nil && !errors.Is(err, tc.cause) {
				t.Errorf("error=%v; want wrapped %v", err, tc.cause)
			}
			if out.Len() != 0 || errOut.Len() != 0 {
				t.Fatalf("unexpected output: stdout=%q stderr=%q", out.String(), errOut.String())
			}
		})
	}
}

func TestSweepRequiresEmptyCacheSupport(t *testing.T) {
	t.Setenv("GH_HOST", "")
	for _, tc := range []struct {
		name, help, want string
		err              error
	}{
		{"old gh", "--all", "upgrade gh", nil},
		{"help fails", "--succeed-on-no-caches", "check gh cache support", errors.New("help failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			help := supportStep()
			help.output, help.err = tc.help, tc.err
			var out, errOut bytes.Buffer
			err := sweep(context.Background(), []string{"--repo", "acme/tool", "--yes"}, scriptedRunner(t, apiStep("repos/acme/tool", false, `{"full_name":"acme/tool"}`), help), &out, &errOut)
			if err == nil {
				t.Fatal("expected support error")
			}
			requireContains(t, err.Error(), tc.want)
		})
	}
}

func TestSweepTimeoutAndCancellation(t *testing.T) {
	t.Setenv("GH_HOST", "")
	wait := func(ctx context.Context) ([]byte, error) { <-ctx.Done(); return nil, ctx.Err() }
	t.Run("discovery timeout", func(t *testing.T) {
		step := apiStep("repos/acme/tool", false, "")
		step.action = wait
		var out, errOut bytes.Buffer
		err := sweep(context.Background(), []string{"--repo", "acme/tool", "--yes", "--timeout", "1ms"}, scriptedRunner(t, step), &out, &errOut)
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error=%v; want deadline exceeded", err)
		}
		requireContains(t, err.Error(), "no deletion attempted")
	})
	t.Run("already canceled discovery", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		step := apiStep("repos/acme/tool", false, "")
		step.action = wait
		var out, errOut bytes.Buffer
		err := sweep(ctx, []string{"--repo", "acme/tool", "--yes"}, scriptedRunner(t, step), &out, &errOut)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v; want canceled", err)
		}
	})
	t.Run("delete timeout continues", func(t *testing.T) {
		step := deleteStep("acme/a")
		step.action = wait
		var out, errOut bytes.Buffer
		err := sweep(context.Background(), []string{"--org", "acme", "--yes", "--timeout", "1ms"}, scriptedRunner(t,
			apiStep("orgs/acme/repos?type=all&per_page=100", true, `[{"full_name":"acme/a"},{"full_name":"acme/b"}]`), supportStep(), step, deleteStep("acme/b")), &out, &errOut)
		if err == nil {
			t.Fatal("expected cleanup failure")
		}
		requireContains(t, errOut.String(), "FAILED acme/a: context deadline exceeded")
		requireContains(t, out.String(), "Summary: selected=2 succeeded=1 failed=1 unattempted=0")
	})
	t.Run("cancel during deletion stops remaining repos", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		step := deleteStep("acme/a")
		step.action = func(child context.Context) ([]byte, error) { cancel(); return wait(child) }
		var out, errOut bytes.Buffer
		err := sweep(ctx, []string{"--org", "acme", "--yes"}, scriptedRunner(t,
			apiStep("orgs/acme/repos?type=all&per_page=100", true, `[{"full_name":"acme/a"},{"full_name":"acme/b"}]`), supportStep(), step), &out, &errOut)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v; want canceled", err)
		}
		requireContains(t, out.String(), "Summary: selected=2 succeeded=0 failed=1 unattempted=1")
		requireContains(t, errOut.String(), "FAILED acme/a: context canceled")
	})
}

func TestCallCancelsChildAfterReturn(t *testing.T) {
	var child context.Context
	wantErr := errors.New("runner error")
	data, err := call(context.Background(), func(ctx context.Context, args ...string) ([]byte, error) {
		child = ctx
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > time.Minute {
			t.Error("missing or incorrect deadline")
		}
		return []byte("partial"), wantErr
	}, options{timeout: time.Minute}, "test")
	if string(data) != "partial" || !errors.Is(err, wantErr) {
		t.Fatalf("call returned %q, %v", data, err)
	}
	if !errors.Is(child.Err(), context.Canceled) {
		t.Fatalf("child context not canceled: %v", child.Err())
	}
}

func TestInvalidOptionsDoNotRunCommands(t *testing.T) {
	t.Setenv("GH_HOST", "")
	for _, args := range [][]string{
		nil, {"--org", "acme", "--repo", "acme/tool"}, {"--org", "acme", "extra"}, {"--unknown"}, {"--org"},
		{"--org", "a/b"}, {"--org", "."}, {"--user", ".."}, {"--user", "@other"}, {"--team", "acme"},
		{"--team", "acme/core/extra"}, {"--repo", "acme/"}, {"--repo", "acme/../tool"}, {"--repo", "acme/a b"},
		{"--org", "acme", "--hostname", "https://github.com"}, {"--org", "acme", "--hostname", "-host"},
		{"--org", "acme", "--timeout", "0s"}, {"--org", "acme", "--timeout", "-1s"}, {"--org", "acme", "--timeout", "invalid"},
		{"--org", "acme", "--yes=invalid"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var out, errOut bytes.Buffer
			if err := sweep(context.Background(), args, scriptedRunner(t), &out, &errOut); err == nil {
				t.Fatal("expected invalid options error")
			}
			if out.Len() != 0 {
				t.Errorf("unexpected stdout=%q", out.String())
			}
		})
	}
}

func TestOptionsDefaultsAndHostname(t *testing.T) {
	t.Setenv("GH_HOST", "")
	o, err := parseOptions([]string{"--org", "acme"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if o.hostname != "github.com" || o.timeout != 10*time.Minute || o.yes || o.dryRun || o.skipArchived {
		t.Fatalf("unexpected defaults: %+v", o)
	}
	t.Setenv("GH_HOST", "env.example.com")
	for _, explicit := range []bool{false, true} {
		args := []string{"--repo", "acme/tool", "--yes", "--timeout", "2s"}
		host := "env.example.com"
		if explicit {
			host = "explicit.example.com"
			args = append(args, "--hostname", host)
		}
		discovery := apiStep("repos/acme/tool", false, `{"full_name":"acme/tool"}`)
		discovery.args[2] = host
		deletion := deleteStep("acme/tool")
		deletion.args[len(deletion.args)-1] = host + "/acme/tool"
		var out, errOut bytes.Buffer
		if err := sweep(context.Background(), args, scriptedRunner(t, discovery, supportStep(), deletion), &out, &errOut); err != nil {
			t.Fatal(err)
		}
		requireContains(t, out.String(), "Selected 1 repositories on "+host+".")
	}
	t.Setenv("GH_HOST", "https://bad.example.com")
	if _, err := parseOptions([]string{"--org", "acme"}, &bytes.Buffer{}); err == nil {
		t.Error("expected invalid environment hostname error")
	}
}

func TestHelpDoesNotRunCommands(t *testing.T) {
	var out, errOut bytes.Buffer
	err := sweep(context.Background(), []string{"--help"}, scriptedRunner(t), &out, &errOut)
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("error=%v; want flag.ErrHelp", err)
	}
	requireContains(t, errOut.String(), "Preview is the default")
}
