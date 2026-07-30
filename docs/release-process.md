# Reactorcide Release Process

Reactorcide uses two workflows to create a release.

## Tag Workflow

The `Reactorcide Release Tag` workflow starts after a pull request merges into
`main`.

The workflow runs one runnerlib lifecycle job. This job:

1. Runs semver-tags in dry-run mode to calculate the next version.
2. Creates a GitHub draft release for the source commit.
3. Runs semver-tags with the CI GitHub credential.
4. Pushes the new version tag.

The job uses the `catalystcommunity/ci:githubpat` secret. The token does not
appear in the Git remote URL or command arguments. Git receives the token
through a temporary askpass program.

## Artifact Workflow

The `Reactorcide Release` workflow starts only for a `tag_created` event whose
tag matches `v*`.

The Reactorcide project must include `tag_created` in `allowed_event_types`.
The project target branch list does not filter tag events.

The prepare job checks all of these conditions:

- The event is `tag_created`.
- The tag has a valid release version.
- A CI-created draft release exists for the source commit.
- The draft release tag matches the pushed tag.
- The Git tag points to the source commit.

These checks prevent a normal tag push from authorizing a release. The CI tag
workflow must create the matching draft first.

After the prepare job succeeds, the image and CLI jobs run in parallel. The
publish job runs after all build jobs succeed.

## Retry Behavior

If the tag job fails before it pushes the tag, retry that job. The job reuses
the draft release and tries the tag push again.

If an artifact job fails, retry the unsuccessful jobs in the artifact
workflow. The build jobs reuse the draft release.

Do not create a release tag by hand. A hand-created tag does not have the
CI-created draft marker, so the prepare job rejects it.

## Bootstrap After a Release Workflow Change

An older coordinator can use the pull request base commit as the trusted CI
source for a merged event. In that case, a release workflow change cannot run
for its own merge.

Use two pull requests:

1. Merge the release workflow change.
2. Add `tag_created` to the live project's allowed events.
3. Temporarily clear the live project's target branch list. The old
   coordinator applies this list to tag events.
4. Merge a second normal change.

The second merge uses the first merge as its trusted CI source. It can run the
new tag workflow. Deploy the new coordinator image. The new coordinator uses
the merge commit for both source code and trusted CI. It also does not apply
the target branch list to tags. You can then restore the target branch list to
`main`. Keep `tag_created` in the allowed event list.
