package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/secrets"
	"github.com/catalystcommunity/reactorcide/coordinator_api/internal/worker"
	"github.com/google/uuid"
	"github.com/urfave/cli/v2"
)

// remoteOnlyOpPatterns flags shell snippets that typically depend on CI-only
// state (auth, remote refs, PR context). Matched against the job command at
// run-local startup to advise authors that the operation may not behave the
// same locally.
var remoteOnlyOpPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bgit\s+push\b`),
	regexp.MustCompile(`\bgit\s+tag\b`),
	regexp.MustCompile(`\bgit\s+commit\b`),
	regexp.MustCompile(`\bgh\s+pr\b`),
	regexp.MustCompile(`\bgh\s+release\b`),
	regexp.MustCompile(`\bgh\s+issue\b`),
	regexp.MustCompile(`\bgh\s+api\b`),
}

// scanRemoteOnlyOps returns the ops detected in cmd that typically depend on
// CI-only state. Empty result means the command looks local-safe.
func scanRemoteOnlyOps(cmd string) []string {
	var hits []string
	for _, p := range remoteOnlyOpPatterns {
		if m := p.FindString(cmd); m != "" {
			hits = append(hits, m)
		}
	}
	return hits
}

// hostRunAsUser returns the uid and gid the container should run as for
// run-local. When invoked via sudo, SUDO_UID/SUDO_GID point at the real user
// so the container doesn't end up running as root and writing root-owned
// files through the bind mount.
func hostRunAsUser() (uid, gid int) {
	uid = os.Getuid()
	gid = os.Getgid()
	if s := os.Getenv("SUDO_UID"); s != "" {
		if v, err := strconv.Atoi(s); err == nil {
			uid = v
		}
	}
	if s := os.Getenv("SUDO_GID"); s != "" {
		if v, err := strconv.Atoi(s); err == nil {
			gid = v
		}
	}
	return uid, gid
}

// parseUserGroup parses a user spec into numeric ids. It accepts a numeric
// "uid[:gid]" (gid defaults to the uid when omitted), or the symbolic names
// "runner" (the image's conventional runner uid, 1001:1001), "root", and "host" (the
// invoking host user). Symbolic names let a job YAML say `run_local: { user:
// runner }` without hard-coding 1001.
func parseUserGroup(s string) (uid, gid int, err error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "runner":
		return runnerUID, runnerGID, nil
	case "root":
		return 0, 0, nil
	case "host":
		uid, gid = hostRunAsUser()
		return uid, gid, nil
	}
	parts := strings.SplitN(strings.TrimSpace(s), ":", 2)
	uid, err = strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, fmt.Errorf("invalid user %q: uid must be an integer", s)
	}
	gid = uid
	if len(parts) == 2 && strings.TrimSpace(parts[1]) != "" {
		gid, err = strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil {
			return 0, 0, fmt.Errorf("invalid user %q: gid must be an integer", s)
		}
	}
	return uid, gid, nil
}

// resolveRunAsUser determines the uid/gid the job container runs as, and
// whether that uid is the image's conventional runner user (1001). Resolution
// order, highest priority first:
//
//  1. --user <uid:gid>      (CLI)
//  2. --as-runner           (CLI)
//  3. run_local.user        (job YAML)
//  4. run_local.as_runner   (job YAML)
//  5. run_as.user           (job YAML)
//  6. host uid              (default, legacy behavior)
//
// CLI flags override the job YAML so a single invocation can deviate, while the
// YAML block keeps every flag-less run-local of that job consistent. When the
// resolved id is the runner uid, asRunner is true and run-local relies on the
// image's own /etc/passwd and sudoers instead of synthesizing them.
func resolveRunAsUser(ctx *cli.Context, spec *worker.JobSpec) (uid, gid int, asRunner bool, err error) {
	var specUser string
	var specAsRunner bool
	if spec != nil && spec.RunLocal != nil {
		specUser = spec.RunLocal.User
		specAsRunner = spec.RunLocal.AsRunner
	}
	var runAsUser string
	if spec != nil && spec.RunAs != nil {
		runAsUser = spec.RunAs.User
	}
	return resolveRunAsUserFromArgs(ctx.String("user"), ctx.Bool("as-runner"), specUser, specAsRunner, runAsUser)
}

// resolveRunAsUserFromArgs is the pure-function core of resolveRunAsUser,
// extracted so the precedence logic can be unit tested without a cli.Context.
// When nothing pins a uid it falls back to the host user.
func resolveRunAsUserFromArgs(userFlag string, asRunnerFlag bool, specUser string, specAsRunner bool, runAsUser string) (uid, gid int, asRunner bool, err error) {
	if asRunnerFlag && userFlag != "" {
		return 0, 0, false, fmt.Errorf("--as-runner and --user are mutually exclusive")
	}

	switch {
	case userFlag != "":
		uid, gid, err = parseUserGroup(userFlag)
	case asRunnerFlag:
		uid, gid = runnerUID, runnerGID
	case specUser != "":
		uid, gid, err = parseUserGroup(specUser)
	case specAsRunner:
		uid, gid = runnerUID, runnerGID
	case runAsUser != "":
		uid, gid, err = parseUserGroup(runAsUser)
	default:
		uid, gid = hostRunAsUser()
	}
	if err != nil {
		return 0, 0, false, err
	}

	asRunner = uid == runnerUID && gid == runnerGID
	return uid, gid, asRunner, nil
}

// makeWritableFor ensures the container's run-as user can write to a run-local
// scratch path. It first tries to chown to the target uid/gid — which works
// when run-local runs as root (e.g. via sudo) or when the target is the host
// uid. When that fails (an unprivileged run-local can't chown to a foreign uid
// such as 1001 for --as-runner) it falls back to a world-writable mode so the
// foreign uid can still write to this throwaway /tmp path.
func makeWritableFor(path string, uid, gid int) error {
	if err := os.Chown(path, uid, gid); err != nil {
		if chmodErr := os.Chmod(path, 0777); chmodErr != nil {
			return fmt.Errorf("could not chown %s to %d:%d (%v) nor make it world-writable: %w", path, uid, gid, err, chmodErr)
		}
	}
	return nil
}

// RunLocalCommand executes a job in a container, fulfilling worker behavior.
// This uses the same JobRunner infrastructure as the worker, ensuring consistent
// execution between local development and production. Or in outages of some kind.
var RunLocalCommand = &cli.Command{
	Name:      "run-local",
	Usage:     "Execute a job in a container locally (emulates worker behavior)",
	ArgsUsage: "<job-file>",
	Flags: []cli.Flag{
		&cli.BoolFlag{
			Name:  "dry-run",
			Usage: "Show what would be executed without running",
		},
		&cli.StringFlag{
			Name:  "job-dir",
			Usage: "Job directory to mount into container (default: ./job)",
			Value: "./job",
		},
		&cli.StringFlag{
			Name:  "backend",
			Usage: "Container runtime backend: docker, containerd, kubernetes",
			Value: "docker",
		},
		&cli.StringSliceFlag{
			Name:    "input",
			Aliases: []string{"i"},
			Usage:   "Overlay YAML files to merge with job spec (first has highest priority)",
		},
		&cli.BoolFlag{
			Name:  "allow-secret-overrides",
			Usage: "Allow overlays to override secret references with plaintext values",
		},
		&cli.StringFlag{
			Name:  "code-url",
			Usage: "Git URL to clone into /job/src instead of bind-mounting --job-dir. Used together with --code-ref to test arbitrary code (e.g. a fork branch) locally.",
		},
		&cli.StringFlag{
			Name:  "code-ref",
			Usage: "Git ref (branch, tag, or SHA) to checkout from --code-url. Defaults to the remote's default branch.",
		},
		&cli.IntFlag{
			Name:  "pr",
			Usage: "GitHub PR number to test. Resolves the head repo and branch via `gh pr view`, then behaves like --code-url / --code-ref. Requires `gh` on PATH and the cwd to be inside a github checkout. Mutually exclusive with --code-url.",
		},
		&cli.BoolFlag{
			Name:  "as-runner",
			Usage: "Run the job container as the image's runner uid (1001:1001), matching the worker, instead of the host uid. Gives sudo/HOME parity for jobs that rely on the image's runner user (e.g. `sudo apt-get`). Writes to /job are made world-writable since an unprivileged run-local can't chown to 1001. Can also be set per-job via the job YAML's run_local block.",
		},
		&cli.StringFlag{
			Name:  "user",
			Usage: "User to run the job container as: a numeric uid[:gid] (e.g. \"1001:1001\"), or the symbolic name \"runner\" (image runner uid 1001), \"root\", or \"host\" (the invoking user). Overrides the host-uid default. Mutually exclusive with --as-runner.",
		},
	},
	Action: runLocalAction,
}

// runnerUID/runnerGID are the image's conventional runner user. The worker
// runs jobs as this uid by default (see docker_runner.go), and the runnerbase
// image creates a `runner` user with this uid plus its sudoers entry. When
// run-local targets this uid it can lean on the image's own /etc/passwd and
// sudoers instead of synthesizing them.
const (
	runnerUID = 1001
	runnerGID = 1001
)

// resolvedCodeSource holds what --code-url / --code-ref / --pr resolve to. A
// nil value means run-local should fall back to its default behavior of
// bind-mounting --job-dir at /job/src.
type resolvedCodeSource struct {
	URL      string
	Ref      string
	HeadRef  string // branch name (== Ref when Ref is a branch; used for HEAD_REF env)
	BaseURL  string // upstream URL when PR is cross-repo, empty otherwise
	BaseRef  string // base branch when known
	PRNumber int    // 0 when not from --pr
	IsForkPR bool
}

// resolveCodeSource turns the user's --code-url / --code-ref / --pr flags
// into a resolvedCodeSource, or returns nil when none were set. Errors on
// mutually-exclusive combinations or when --pr resolution fails.
func resolveCodeSource(ctx *cli.Context) (*resolvedCodeSource, error) {
	return resolveCodeSourceFromArgs(ctx.String("code-url"), ctx.String("code-ref"), ctx.Int("pr"))
}

// resolveCodeSourceFromArgs is the pure-function core of resolveCodeSource,
// extracted so it can be unit tested without constructing a cli.Context.
func resolveCodeSourceFromArgs(codeURL, codeRef string, prNum int) (*resolvedCodeSource, error) {
	if prNum != 0 && codeURL != "" {
		return nil, fmt.Errorf("--pr and --code-url are mutually exclusive")
	}

	if codeURL != "" {
		return &resolvedCodeSource{
			URL:     codeURL,
			Ref:     codeRef,
			HeadRef: codeRef,
		}, nil
	}

	if prNum != 0 {
		return resolvePRViaGH(prNum)
	}

	return nil, nil
}

// resolvePRViaGH shells out to `gh pr view` from the cwd to discover the
// PR's head/base repos and branches. Returns a friendly error if `gh` is
// missing or the cwd isn't a recognizable GitHub checkout.
func resolvePRViaGH(prNum int) (*resolvedCodeSource, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return nil, fmt.Errorf("--pr requires the `gh` CLI on PATH; install it from https://cli.github.com or use --code-url instead")
	}

	cmd := exec.Command("gh", "pr", "view", strconv.Itoa(prNum),
		"--json", "headRefName,headRefOid,baseRefName,isCrossRepository,headRepository,headRepositoryOwner,baseRepository")
	out, err := cmd.Output()
	if err != nil {
		stderr := ""
		if ee, ok := err.(*exec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		return nil, fmt.Errorf("`gh pr view %d` failed: %w (stderr: %s) — run from inside a checkout of the PR's target repo, or use --code-url", prNum, err, stderr)
	}

	var pr struct {
		HeadRefName       string `json:"headRefName"`
		HeadRefOid        string `json:"headRefOid"`
		BaseRefName       string `json:"baseRefName"`
		IsCrossRepository bool   `json:"isCrossRepository"`
		HeadRepository    struct {
			Name          string `json:"name"`
			NameWithOwner string `json:"nameWithOwner"`
		} `json:"headRepository"`
		HeadRepositoryOwner struct {
			Login string `json:"login"`
		} `json:"headRepositoryOwner"`
		BaseRepository struct {
			Name          string `json:"name"`
			NameWithOwner string `json:"nameWithOwner"`
		} `json:"baseRepository"`
	}
	if err := json.Unmarshal(out, &pr); err != nil {
		return nil, fmt.Errorf("could not parse `gh pr view` output: %w", err)
	}

	// `gh pr view` returns Name for head/base repos but not always a full
	// nameWithOwner; fall back to combining owner + name when needed.
	headOwner := pr.HeadRepositoryOwner.Login
	headNameWithOwner := pr.HeadRepository.NameWithOwner
	if headNameWithOwner == "" && headOwner != "" && pr.HeadRepository.Name != "" {
		headNameWithOwner = headOwner + "/" + pr.HeadRepository.Name
	}
	if headNameWithOwner == "" {
		return nil, fmt.Errorf("`gh pr view %d` returned no head repository info", prNum)
	}
	baseNameWithOwner := pr.BaseRepository.NameWithOwner
	if baseNameWithOwner == "" {
		// Best effort: derive from `gh repo view`.
		if rc, err := exec.Command("gh", "repo", "view", "--json", "nameWithOwner").Output(); err == nil {
			var rv struct {
				NameWithOwner string `json:"nameWithOwner"`
			}
			_ = json.Unmarshal(rc, &rv)
			baseNameWithOwner = rv.NameWithOwner
		}
	}

	src := &resolvedCodeSource{
		URL:      fmt.Sprintf("https://github.com/%s.git", headNameWithOwner),
		Ref:      pr.HeadRefName,
		HeadRef:  pr.HeadRefName,
		BaseRef:  pr.BaseRefName,
		PRNumber: prNum,
		IsForkPR: pr.IsCrossRepository,
	}
	if baseNameWithOwner != "" {
		src.BaseURL = fmt.Sprintf("https://github.com/%s.git", baseNameWithOwner)
	}
	return src, nil
}

// cloneCodeSource clones src.URL@src.Ref into a fresh tempdir and returns the
// tempdir path plus a cleanup func the caller must defer.
func cloneCodeSource(src *resolvedCodeSource, uid, gid int) (string, func(), error) {
	tmp, err := os.MkdirTemp("/tmp", "reactorcide-local-code-")
	if err != nil {
		return "", func() {}, fmt.Errorf("failed to create temp dir for --code-url checkout: %w", err)
	}
	cleanup := func() { os.RemoveAll(tmp) }

	args := []string{"clone"}
	if src.Ref != "" {
		args = append(args, "--branch", src.Ref)
	}
	args = append(args, src.URL, tmp)
	cmd := exec.Command("git", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		cleanup()
		return "", func() {}, fmt.Errorf("git clone failed for %s @ %s: %w", src.URL, src.Ref, err)
	}

	// If a base URL was supplied (cross-repo PR) and it differs from the
	// cloned URL, add it as `upstream` and fetch the base ref. This mirrors
	// what runnerlib's _checkout_with_fetch_fallback does for the worker.
	if src.BaseURL != "" && src.BaseURL != src.URL {
		if err := exec.Command("git", "-C", tmp, "remote", "add", "upstream", src.BaseURL).Run(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to add upstream remote %s: %v\n", src.BaseURL, err)
		} else if src.BaseRef != "" {
			if err := exec.Command("git", "-C", tmp, "fetch", "upstream", src.BaseRef).Run(); err != nil {
				fmt.Fprintf(os.Stderr, "Warning: failed to fetch upstream/%s: %v\n", src.BaseRef, err)
			}
		}
	}

	// Match ownership to the container's run-as user. When run-local is
	// unprivileged it can't chown to a foreign uid (e.g. 1001 for --as-runner);
	// in that case add group/other write bits so that uid can still write to
	// this throwaway clone, preserving existing exec bits (scripts, binaries).
	_ = filepath.Walk(tmp, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info == nil {
			return nil
		}
		if err := os.Chown(path, uid, gid); err != nil {
			add := os.FileMode(0066)
			if info.IsDir() {
				add = 0077
			}
			_ = os.Chmod(path, info.Mode()|add)
		}
		return nil
	})
	return tmp, cleanup, nil
}

func runLocalAction(ctx *cli.Context) error {
	if ctx.NArg() < 1 {
		return fmt.Errorf("usage: reactorcide run-local <job-file>")
	}

	jobFile := ctx.Args().Get(0)
	dryRun := ctx.Bool("dry-run")
	jobDir := ctx.String("job-dir")
	backend := ctx.String("backend")
	inputFiles := ctx.StringSlice("input")
	allowSecretOverrides := ctx.Bool("allow-secret-overrides")

	codeSource, err := resolveCodeSource(ctx)
	if err != nil {
		return err
	}

	// Load job specification with overlays
	spec, secretOverrides, err := worker.LoadJobSpecWithOverlays(jobFile, inputFiles)
	if err != nil {
		return err
	}

	// Refuse to execute jobs explicitly marked as remote-only.
	if spec.DisableRunLocal {
		return fmt.Errorf(
			"job %q is marked disable_run_local: true and cannot be executed via run-local "+
				"(it depends on CI-only state — e.g. PR diff base, push back to remote)",
			spec.Name,
		)
	}

	// Check for secret overrides
	if err := checkSecretOverrides(secretOverrides, allowSecretOverrides); err != nil {
		return err
	}

	// Mark this run as local so the job (and runnerlib, when wrapped) can
	// branch on it — e.g. to skip an in-job git clone in favor of the
	// bind-mounted /job/src.
	if spec.Environment == nil {
		spec.Environment = map[string]string{}
	}
	spec.Environment["REACTORCIDE_WORKER_MODE"] = "local"

	// Advisory: warn if the command contains operations that typically need
	// CI state (push, tag, commit, gh write ops). Non-blocking — the author
	// may already gate these on REACTORCIDE_WORKER_MODE.
	if hits := scanRemoteOnlyOps(spec.Command); len(hits) > 0 {
		fmt.Fprintf(os.Stderr,
			"WARNING: job command contains remote-only operations (%v). "+
				"These may not behave the same locally — auth, remote refs, or PR context "+
				"may be missing. Consider gating on $REACTORCIDE_WORKER_MODE, or marking the "+
				"job disable_run_local: true if it cannot run locally at all.\n",
			hits,
		)
	}

	fmt.Printf("Job: %s\n", spec.Name)
	fmt.Printf("Image: %s\n", spec.Image)
	fmt.Printf("Command: %s\n", spec.Command)

	// Resolve absolute path for job directory
	absJobDir, err := filepath.Abs(jobDir)
	if err != nil {
		return fmt.Errorf("failed to resolve job directory: %w", err)
	}

	// Validate job directory exists (don't create it — it's the user's source code)
	if info, err := os.Stat(absJobDir); err != nil {
		return fmt.Errorf("job directory does not exist: %s", absJobDir)
	} else if !info.IsDir() {
		return fmt.Errorf("job directory is not a directory: %s", absJobDir)
	}

	// First resolve ${env:VAR_NAME} references from host environment
	spec.Environment = worker.ResolveEnvInMap(spec.Environment)

	// Then resolve ${secret:path:key} references
	resolvedEnv, secretValues, err := resolveJobSecrets(spec.Environment)
	if err != nil {
		return err
	}
	spec.Environment = resolvedEnv

	// Create a masker for secret values
	masker := secrets.NewMasker()
	for _, sv := range secretValues {
		masker.RegisterSecret(sv)
	}
	// Also mask any values that look like secrets based on key names
	for k, v := range spec.Environment {
		if isSensitiveKey(k) {
			masker.RegisterSecret(v)
		}
	}

	// Generate a job ID for this execution
	jobID := uuid.New().String()[:8]

	// Resolve the uid/gid the job container runs as. Default is the host user
	// (legacy run-local behavior); --as-runner / --user or the job's run_local
	// block can pin the worker's runner uid (1001) for sudo/HOME parity.
	uid, gid, asRunner, err := resolveRunAsUser(ctx, spec)
	if err != nil {
		return err
	}
	if asRunner {
		fmt.Printf("Run-as user: %d:%d (image runner — worker parity)\n", uid, gid)
	} else if uid == 0 && gid == 0 {
		fmt.Printf("Run-as user: %d:%d (root)\n", uid, gid)
	} else {
		fmt.Printf("Run-as user: %d:%d (host)\n", uid, gid)
	}

	// Create temp workspace matching production layout: /job/ with src/ subdir.
	// The container runs as the resolved uid, so make the workspace and its
	// src/ writable by that uid — otherwise the container can't write
	// triggers.json, logs, etc. (e.g. when run-local is invoked via sudo, or
	// when --as-runner pins a uid that doesn't own this tmpdir).
	tempWorkspace, err := os.MkdirTemp("/tmp", fmt.Sprintf("reactorcide-local-job-%s-", jobID))
	if err != nil {
		return fmt.Errorf("failed to create temp workspace: %w", err)
	}
	if err := os.Chmod(tempWorkspace, 0755); err != nil {
		return fmt.Errorf("failed to chmod temp workspace: %w", err)
	}
	if err := makeWritableFor(tempWorkspace, uid, gid); err != nil {
		return fmt.Errorf("failed to prepare temp workspace: %w", err)
	}
	defer os.RemoveAll(tempWorkspace)

	codeDir := worker.ContainerPathInsideJob(worker.DefaultJobCodeDir(spec.CodeDir))
	if codeDir == "." {
		codeDir = "src"
	}
	codeSubdir := filepath.Join(tempWorkspace, codeDir)
	if err := os.MkdirAll(codeSubdir, 0755); err != nil {
		return fmt.Errorf("failed to create workspace code dir: %w", err)
	}
	if err := makeWritableFor(codeSubdir, uid, gid); err != nil {
		return fmt.Errorf("failed to prepare workspace code dir: %w", err)
	}

	// Resolve what to mount at the configured code directory:
	//   - default: --job-dir (the user's local working copy)
	//   - --code-url / --pr: clone into a tempdir, mount that instead
	srcMount := absJobDir
	if codeSource != nil {
		fmt.Printf("Code source: cloning %s @ %s\n", codeSource.URL, codeSource.Ref)
		if spec.Source != nil && spec.Source.Type != "" && spec.Source.Type != "none" {
			fmt.Fprintf(os.Stderr,
				"Notice: job spec declares source.%s but --code-url/--pr overrides it.\n",
				spec.Source.Type,
			)
		}
		codeDir, cleanup, err := cloneCodeSource(codeSource, uid, gid)
		if err != nil {
			return err
		}
		defer cleanup()
		srcMount = codeDir

		// Populate the same env vars the worker would set so the job
		// behaves identically whether run locally or remotely.
		spec.Environment["REACTORCIDE_SOURCE_URL"] = codeSource.URL
		spec.Environment["REACTORCIDE_HEAD_URL"] = codeSource.URL
		if codeSource.HeadRef != "" {
			spec.Environment["REACTORCIDE_HEAD_REF"] = codeSource.HeadRef
			spec.Environment["REACTORCIDE_PR_REF"] = codeSource.HeadRef
		}
		if codeSource.BaseURL != "" {
			spec.Environment["REACTORCIDE_BASE_URL"] = codeSource.BaseURL
		}
		if codeSource.BaseRef != "" {
			spec.Environment["REACTORCIDE_BASE_REF"] = codeSource.BaseRef
			spec.Environment["REACTORCIDE_PR_BASE_REF"] = codeSource.BaseRef
		}
		if codeSource.IsForkPR {
			spec.Environment["REACTORCIDE_IS_FORK_PR"] = "true"
		}
		if codeSource.PRNumber > 0 {
			spec.Environment["REACTORCIDE_PR_NUMBER"] = strconv.Itoa(codeSource.PRNumber)
		}
	}

	// Convert spec to JobConfig using temp workspace, then set SourceDir
	// so the runner mounts the resolved source at the configured code dir.
	jobConfig := spec.ToJobConfig(tempWorkspace, jobID, "local")
	jobConfig.SourceDir = srcMount
	jobConfig.RunAsUser = fmt.Sprintf("%d:%d", uid, gid)

	// When running as the image's runner uid (--as-runner / --user 1001:1001),
	// the container already has a real /etc/passwd entry (`runner`, home
	// /home/runner, owned by 1001 and writable) and the matching sudoers entry,
	// so we leave them alone and let HOME resolve from the image — exactly what
	// the worker does. Otherwise we synthesize a minimal /etc/passwd and
	// /etc/group so tools that require a passwd entry (ssh, sudo, id, etc.)
	// work when the container runs as a uid that doesn't match any user baked
	// into the image, plus a writable HOME scratch at /home/runner so ~ is both
	// writable and the same path the worker uses.
	if !asRunner {
		controlDir, err := os.MkdirTemp("/tmp", fmt.Sprintf("reactorcide-local-ctl-%s-", jobID))
		if err != nil {
			return fmt.Errorf("failed to create control dir: %w", err)
		}
		defer os.RemoveAll(controlDir)

		// Writable HOME scratch owned by the run-as (host) uid, mounted at
		// /home/runner. The image's own /home/runner is owned by 1001 and so
		// isn't writable by the host uid; this gives a fresh, run-as-uid-owned
		// HOME the way each container starts with an empty ephemeral home on a
		// worker.
		homeDir := filepath.Join(controlDir, "home")
		if err := os.MkdirAll(homeDir, 0755); err != nil {
			return fmt.Errorf("failed to create home scratch dir: %w", err)
		}
		if err := makeWritableFor(homeDir, uid, gid); err != nil {
			return fmt.Errorf("failed to prepare home scratch dir: %w", err)
		}

		passwdFile := filepath.Join(controlDir, "passwd")
		groupFile := filepath.Join(controlDir, "group")
		passwdContents := fmt.Sprintf(
			"root:x:0:0:root:/root:/bin/sh\n"+
				"reactorcide:x:1001:1001:reactorcide:/home/reactorcide:/bin/sh\n"+
				"local:x:%d:%d:local user:/home/runner:/bin/sh\n",
			uid, gid,
		)
		groupContents := fmt.Sprintf(
			"root:x:0:\n"+
				"reactorcide:x:1001:\n"+
				"local:x:%d:\n",
			gid,
		)
		if err := os.WriteFile(passwdFile, []byte(passwdContents), 0644); err != nil {
			return fmt.Errorf("failed to write synthetic passwd: %w", err)
		}
		if err := os.WriteFile(groupFile, []byte(groupContents), 0644); err != nil {
			return fmt.Errorf("failed to write synthetic group: %w", err)
		}
		jobConfig.ExtraMounts = append(jobConfig.ExtraMounts,
			fmt.Sprintf("%s:/etc/passwd:ro", passwdFile),
			fmt.Sprintf("%s:/etc/group:ro", groupFile),
			fmt.Sprintf("%s:/home/runner", homeDir),
		)

		// Default HOME to the writable scratch (matches the worker's
		// /home/runner). A job that sets HOME explicitly still wins.
		if _, ok := jobConfig.Env["HOME"]; !ok {
			jobConfig.Env["HOME"] = "/home/runner"
		}
	}

	if dryRun {
		return performLocalDryRun(spec, jobConfig, masker, srcMount)
	}

	// Create the job runner
	runner, err := worker.NewJobRunner(backend)
	if err != nil {
		return fmt.Errorf("failed to create job runner: %w", err)
	}

	// Execute the job
	return executeLocalJob(context.Background(), runner, jobConfig, masker)
}

func performLocalDryRun(spec *worker.JobSpec, config *worker.JobConfig, masker *secrets.Masker, jobDir string) error {
	fmt.Println("\n--- DRY RUN MODE ---")
	fmt.Printf("Image: %s\n", spec.Image)
	fmt.Printf("Command: %s\n", spec.Command)
	sourceMountPath := config.SourceMountPath
	if sourceMountPath == "" {
		sourceMountPath = "/job/src"
	}
	fmt.Printf("Source directory: %s -> %s\n", jobDir, sourceMountPath)
	fmt.Printf("Workspace: %s -> /job\n", config.WorkspaceDir)

	if spec.Source != nil && spec.Source.Type != "" && spec.Source.Type != "none" {
		fmt.Printf("Source: %s from %s (ref: %s)\n",
			spec.Source.Type, spec.Source.URL, spec.Source.Ref)
	}

	fmt.Println("\nEnvironment variables:")
	for k, v := range spec.Environment {
		masked := masker.MaskString(v)
		fmt.Printf("  %s=%s\n", k, masked)
	}

	if len(spec.Capabilities) > 0 {
		fmt.Println("\nCapabilities:")
		for _, cap := range spec.Capabilities {
			fmt.Printf("  - %s\n", cap)
		}
	}

	fmt.Println("\nJobConfig:")
	fmt.Printf("  Image: %s\n", config.Image)
	fmt.Printf("  Command: %v\n", config.Command)
	fmt.Printf("  WorkspaceDir: %s\n", config.WorkspaceDir)
	fmt.Printf("  SourceDir: %s\n", config.SourceDir)
	fmt.Printf("  WorkingDir: %s\n", config.WorkingDir)
	fmt.Printf("  RunAsUser: %s\n", config.RunAsUser)
	fmt.Printf("  HOME: %s\n", config.Env["HOME"])
	if len(config.ExtraMounts) > 0 {
		fmt.Println("  ExtraMounts:")
		for _, m := range config.ExtraMounts {
			fmt.Printf("    - %s\n", m)
		}
	}

	fmt.Println("\n--- END DRY RUN ---")
	return nil
}

func executeLocalJob(ctx context.Context, runner worker.JobRunner, config *worker.JobConfig, masker *secrets.Masker) error {
	fmt.Printf("\nRunning container: %s\n", config.Image)
	fmt.Println("---")

	// Spawn the job container
	containerID, err := runner.SpawnJob(ctx, config)
	if err != nil {
		return fmt.Errorf("failed to spawn container: %w", err)
	}

	// Ensure cleanup
	defer func() {
		if cleanupErr := runner.Cleanup(context.Background(), containerID); cleanupErr != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to cleanup container: %v\n", cleanupErr)
		}
	}()

	// Stream logs
	stdout, stderr, err := runner.StreamLogs(ctx, containerID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to stream logs: %v\n", err)
	}

	// Stream output with masking
	done := make(chan struct{}, 2)

	if stdout != nil {
		go func() {
			defer stdout.Close()
			scanner := bufio.NewScanner(stdout)
			for scanner.Scan() {
				line := masker.MaskString(scanner.Text())
				fmt.Println(line)
			}
			done <- struct{}{}
		}()
	} else {
		done <- struct{}{}
	}

	if stderr != nil {
		go func() {
			defer stderr.Close()
			scanner := bufio.NewScanner(stderr)
			for scanner.Scan() {
				line := masker.MaskString(scanner.Text())
				fmt.Fprintln(os.Stderr, line)
			}
			done <- struct{}{}
		}()
	} else {
		done <- struct{}{}
	}

	// Wait for log streaming to finish
	<-done
	<-done

	// Wait for completion
	exitCode, err := runner.WaitForCompletion(ctx, containerID)
	fmt.Println("---")

	if err != nil {
		return fmt.Errorf("job execution error: %w", err)
	}

	// Check for triggered jobs
	triggersFile := filepath.Join(config.WorkspaceDir, "triggers.json")
	if _, statErr := os.Stat(triggersFile); statErr == nil {
		data, readErr := os.ReadFile(triggersFile)
		if readErr == nil && len(data) > 0 {
			fmt.Printf("\nTriggered jobs written to: %s\n", triggersFile)
		}
	}

	if exitCode != 0 {
		return cli.Exit(fmt.Sprintf("Job failed with exit code %d", exitCode), exitCode)
	}

	fmt.Println("Job completed successfully")
	return nil
}
