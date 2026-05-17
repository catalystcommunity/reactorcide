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
	},
	Action: runLocalAction,
}

// resolvedCodeSource holds what --code-url / --code-ref / --pr resolve to. A
// nil value means run-local should fall back to its default behavior of
// bind-mounting --job-dir at /job/src.
type resolvedCodeSource struct {
	URL       string
	Ref       string
	HeadRef   string // branch name (== Ref when Ref is a branch; used for HEAD_REF env)
	BaseURL   string // upstream URL when PR is cross-repo, empty otherwise
	BaseRef   string // base branch when known
	PRNumber  int    // 0 when not from --pr
	IsForkPR  bool
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
		HeadRefName         string `json:"headRefName"`
		HeadRefOid          string `json:"headRefOid"`
		BaseRefName         string `json:"baseRefName"`
		IsCrossRepository   bool   `json:"isCrossRepository"`
		HeadRepository      struct {
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

	// Match ownership to the container's run-as user.
	_ = filepath.Walk(tmp, func(path string, _ os.FileInfo, _ error) error {
		_ = os.Chown(path, uid, gid)
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

	// Create temp workspace matching production layout: /job/ with src/ subdir.
	// The container runs as the host user (see hostRunAsUser below), so chown
	// the workspace and its src/ to match — otherwise, when run-local is
	// invoked via sudo, the tmpdir is root-owned and the container can't write
	// triggers.json, logs, etc.
	uid, gid := hostRunAsUser()
	tempWorkspace, err := os.MkdirTemp("/tmp", fmt.Sprintf("reactorcide-local-job-%s-", jobID))
	if err != nil {
		return fmt.Errorf("failed to create temp workspace: %w", err)
	}
	if err := os.Chmod(tempWorkspace, 0755); err != nil {
		return fmt.Errorf("failed to chmod temp workspace: %w", err)
	}
	if err := os.Chown(tempWorkspace, uid, gid); err != nil {
		return fmt.Errorf("failed to chown temp workspace: %w", err)
	}
	defer os.RemoveAll(tempWorkspace)

	// Create src/ subdir in temp workspace (matching production layout)
	srcSubdir := filepath.Join(tempWorkspace, "src")
	if err := os.MkdirAll(srcSubdir, 0755); err != nil {
		return fmt.Errorf("failed to create workspace src dir: %w", err)
	}
	if err := os.Chown(srcSubdir, uid, gid); err != nil {
		return fmt.Errorf("failed to chown workspace src dir: %w", err)
	}

	// Resolve what to mount at /job/src:
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
	// so the runner mounts the resolved source at /job/src.
	jobConfig := spec.ToJobConfig(tempWorkspace, jobID, "local")
	jobConfig.SourceDir = srcMount
	jobConfig.RunAsUser = fmt.Sprintf("%d:%d", uid, gid)

	// Synthesize a minimal /etc/passwd and /etc/group so tools that require a
	// passwd entry (ssh, sudo, id, etc.) work when the container runs as the
	// host uid — which rarely matches any user baked into the image. We keep
	// entries for root and for the image's conventional uid 1001 so references
	// to /home/reactorcide still resolve.
	controlDir, err := os.MkdirTemp("/tmp", fmt.Sprintf("reactorcide-local-ctl-%s-", jobID))
	if err != nil {
		return fmt.Errorf("failed to create control dir: %w", err)
	}
	defer os.RemoveAll(controlDir)

	passwdFile := filepath.Join(controlDir, "passwd")
	groupFile := filepath.Join(controlDir, "group")
	passwdContents := fmt.Sprintf(
		"root:x:0:0:root:/root:/bin/sh\n"+
			"reactorcide:x:1001:1001:reactorcide:/home/reactorcide:/bin/sh\n"+
			"local:x:%d:%d:local user:/job:/bin/sh\n",
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
	)

	// Default HOME to /job so jobs that touch ~ (e.g. mkdir ~/.ssh) work even
	// when the host uid's passwd entry points at /job (see above). CI runs as
	// uid 1001 which has /home/reactorcide; local runs as an arbitrary uid so
	// we anchor ~ to the workspace root.
	if _, ok := jobConfig.Env["HOME"]; !ok {
		jobConfig.Env["HOME"] = "/job"
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
	fmt.Printf("Source directory: %s -> /job/src\n", jobDir)
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
