package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"sort"
	"strings"
	"time"
)

type runner func(context.Context, ...string) ([]byte, error)

type options struct {
	org, user, team, repo, hostname    string
	yes, dryRun, skipArchived, version bool
	timeout                            time.Duration
}

type repository struct {
	FullName string `json:"full_name"`
	Archived bool   `json:"archived"`
}

var component = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

func validName(name string, parts int) bool {
	segments := strings.Split(name, "/")
	if len(segments) != parts {
		return false
	}
	for _, s := range segments {
		if !component.MatchString(s) || s == "." || s == ".." {
			return false
		}
	}
	return true
}

func parseOptions(args []string, out io.Writer) (options, error) {
	var o options
	fs := flag.NewFlagSet("cache-sweep", flag.ContinueOnError)
	fs.SetOutput(out)
	fs.StringVar(&o.org, "org", "", "Sweep repositories owned by an organization")
	fs.StringVar(&o.user, "user", "", "Sweep repositories owned by a user (use @me for yourself)")
	fs.StringVar(&o.team, "team", "", "Sweep repositories accessible to a team: ORG/TEAM-SLUG")
	fs.StringVar(&o.repo, "repo", "", "Sweep a single repository: OWNER/REPO")
	fs.StringVar(&o.hostname, "hostname", "", "GitHub hostname (defaults to GH_HOST or github.com)")
	fs.BoolVar(&o.version, "version", false, "Print version, build time, and repository URL, then exit")
	fs.BoolVar(&o.yes, "yes", false, "Approve deletion; without this flag only preview repositories")
	fs.BoolVar(&o.dryRun, "dry-run", false, "Preview repositories without deleting, even with --yes")
	fs.BoolVar(&o.skipArchived, "skip-archived", false, "Exclude archived repositories")
	fs.DurationVar(&o.timeout, "timeout", 10*time.Minute, "Timeout per gh command, including paginated discovery")
	fs.Usage = func() {
		fmt.Fprintln(out, "Usage: gh cache-sweep (--org ORG | --user USER | --team ORG/SLUG | --repo OWNER/REPO) [flags]")
		fmt.Fprintln(out, "\nPreview is the default. Pass --yes to delete all Actions caches in selected repositories.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return o, err
	}
	if fs.NArg() != 0 {
		return o, errors.New("unexpected positional arguments; use --org, --user, --team, or --repo")
	}
	if o.version {
		return o, nil
	}
	count := 0
	for _, s := range []string{o.org, o.user, o.team, o.repo} {
		if s != "" {
			count++
		}
	}
	if count != 1 {
		return o, errors.New("specify exactly one of --org, --user, --team, or --repo")
	}
	if (o.org != "" && !validName(o.org, 1)) ||
		(o.user != "" && o.user != "@me" && !validName(o.user, 1)) ||
		(o.team != "" && !validName(o.team, 2)) ||
		(o.repo != "" && !validName(o.repo, 2)) {
		return o, errors.New("invalid scope name; team and repo require two slash-separated components")
	}
	if o.hostname == "" {
		o.hostname = os.Getenv("GH_HOST")
		if o.hostname == "" {
			o.hostname = "github.com"
		}
	}
	if !validName(o.hostname, 1) || strings.HasPrefix(o.hostname, "-") {
		return o, errors.New("hostname must be a host name, not a URL")
	}
	if o.timeout <= 0 {
		return o, errors.New("timeout must be positive")
	}
	return o, nil
}

func runGH(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "gh", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	// Never allow an unattended sweep to block on a gh prompt.
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GH_PROMPT_DISABLED=") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	cmd.Env = append(cmd.Env, "GH_PROMPT_DISABLED=1")
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("gh %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

func call(ctx context.Context, run runner, o options, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, o.timeout)
	defer cancel()
	return run(ctx, args...)
}

func api(ctx context.Context, run runner, o options, endpoint string, paginate bool) ([]byte, error) {
	args := []string{"api", "--hostname", o.hostname, "--method", "GET", endpoint}
	if paginate {
		args = append(args, "--paginate")
	}
	return call(ctx, run, o, args...)
}

func discover(ctx context.Context, run runner, o options) ([]repository, error) {
	var endpoint string
	switch {
	case o.repo != "":
		data, err := api(ctx, run, o, "repos/"+o.repo, false)
		if err != nil {
			return nil, err
		}
		var r repository
		if err := json.Unmarshal(data, &r); err != nil {
			return nil, fmt.Errorf("decode repository: %w", err)
		}
		return selectRepos([]repository{r}, o)
	case o.org != "":
		endpoint = "orgs/" + o.org + "/repos?type=all&per_page=100"
	case o.team != "":
		parts := strings.SplitN(o.team, "/", 2)
		endpoint = "orgs/" + parts[0] + "/teams/" + parts[1] + "/repos?per_page=100"
	case o.user != "":
		data, err := api(ctx, run, o, "user", false)
		if err != nil {
			return nil, err
		}
		var viewer struct {
			Login string `json:"login"`
		}
		if err := json.Unmarshal(data, &viewer); err != nil || viewer.Login == "" {
			return nil, errors.New("could not decode authenticated user")
		}
		if o.user == "@me" {
			o.user = viewer.Login
		}
		// The public /users/{name}/repos endpoint omits private repositories.
		// Instead list authenticated ownership/collaborations and filter by owner.
		endpoint = "user/repos?affiliation=owner,collaborator&visibility=all&per_page=100"
	}
	data, err := api(ctx, run, o, endpoint, true)
	if err != nil {
		// Even if gh returned some pages, discovery must finish before deletion.
		return nil, err
	}
	var repos []repository
	dec := json.NewDecoder(bytes.NewReader(data))
	for {
		var page []repository
		err := dec.Decode(&page)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("decode repository page: %w", err)
		}
		repos = append(repos, page...)
	}
	return selectRepos(repos, o)
}

func selectRepos(repos []repository, o options) ([]repository, error) {
	seen := make(map[string]bool)
	selected := make([]repository, 0, len(repos))
	for _, r := range repos {
		if !validName(r.FullName, 2) {
			return nil, fmt.Errorf("invalid repository name in API response: %q", r.FullName)
		}
		if o.user != "" && !strings.EqualFold(strings.SplitN(r.FullName, "/", 2)[0], o.user) {
			continue
		}
		key := strings.ToLower(r.FullName)
		if seen[key] || (o.skipArchived && r.Archived) {
			continue
		}
		seen[key] = true
		selected = append(selected, r)
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].FullName < selected[j].FullName })
	return selected, nil
}

func sweep(ctx context.Context, args []string, run runner, out, errOut io.Writer) error {
	o, err := parseOptions(args, errOut)
	if err != nil {
		return err
	}
	if o.version {
		_, err := io.WriteString(out, versionInfo())
		return err
	}
	repos, err := discover(ctx, run, o)
	if err != nil {
		return fmt.Errorf("repository discovery failed (no deletion attempted): %w", err)
	}
	fmt.Fprintf(out, "Selected %d repositories on %s.\n", len(repos), o.hostname)
	if !o.yes || o.dryRun {
		for _, r := range repos {
			fmt.Fprintf(out, "Would delete all Actions caches: %s\n", r.FullName)
		}
		fmt.Fprintln(out, "Preview only: no caches deleted. Pass --yes without --dry-run to delete.")
		return nil
	}
	if len(repos) == 0 {
		return nil
	}
	// Fail before any deletion if gh is too old to support empty-cache success.
	help, err := call(ctx, run, o, "cache", "delete", "--help")
	if err != nil {
		return fmt.Errorf("check gh cache support: %w", err)
	}
	if !bytes.Contains(help, []byte("--succeed-on-no-caches")) {
		return errors.New("upgrade gh: cache delete must support --succeed-on-no-caches")
	}
	succeeded, failed := 0, 0
	for _, r := range repos {
		if ctx.Err() != nil {
			break
		}
		fmt.Fprintf(out, "Deleting Actions caches: %s\n", r.FullName)
		_, err := call(ctx, run, o, "cache", "delete", "--all", "--succeed-on-no-caches", "--repo", o.hostname+"/"+r.FullName)
		if err != nil {
			failed++
			fmt.Fprintf(errOut, "FAILED %s: %v\n", r.FullName, err)
			continue
		}
		succeeded++
	}
	fmt.Fprintf(out, "Summary: selected=%d succeeded=%d failed=%d unattempted=%d\n", len(repos), succeeded, failed, len(repos)-succeeded-failed)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if failed > 0 {
		return fmt.Errorf("cache cleanup failed for %d repositories; see FAILED lines above", failed)
	}
	return nil
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if err := sweep(ctx, os.Args[1:], runGH, os.Stdout, os.Stderr); err != nil && !errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}
