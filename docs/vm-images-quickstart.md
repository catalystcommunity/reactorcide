# VM Images Quick Start

This guide shows the full procedure for a user VM image:

1. Create a base image.
2. Put the base image in a store: an OCI registry or S3.
3. Build a derived image with your tools.
4. Publish the derived image.
5. Set an alias for jobs.

Reactorcide does not publish base images. Reactorcide gives you the tools and
the instructions to make your base images and to keep them in a store that you
select. The license of the operating system applies to your images.

For more information, see:

- [VM Image Operations](./vm-images.md) for the full command reference.
- [Image Aliases and Worker Image Sets](./image-aliases.md) for aliases,
  rollouts, and the image state of workers.
- [macOS VM Runner](./vm-runners-macos.md) for the macOS host.
- [Windows VM Image Build](./windows-vm-image-build.md) and
  [Windows VM Runner](./vm-runners-windows.md) for the Windows host.

## Image references

A reference gives the location and the content digest of an image:

```text
registry.example.com/team/vm/windows-11:base@sha256:DIGEST
oci://registry.example.com/team/vm/windows-11:base@sha256:DIGEST
s3://bucket/prefix/windows-11:base@sha256:DIGEST
```

The tag (`base`) is a name. A tag can be any text, and a hash is a valid tag.
The digest (`sha256:...`) identifies the content. The publish command prints
the reference with the digest. Use that pinned reference for an image alias
and as the base of derived builds. Jobs name the alias. A worker refuses an
`s3://` reference that has no digest.

The OCI registry and S3 keep the same bytes. The same bundle has the same
digest in the two stores.

## Step 1: Create a base image

A base image contains the operating system, the guest job account, and the
Reactorcide guest daemon (`vmguest`). Do this step one time for each
operating system release.

### macOS

Use an Apple silicon Mac with Virtualization.framework.

1. Create the bootstrap VM and complete Setup Assistant. See
   [macOS VM Runner](./vm-runners-macos.md).
2. Shut down the VM.
3. Build the guest daemon and install it in the stopped bundle:

   ```sh
   cd coordinator_api
   GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -o vmguest ./cmd/vmguest
   sudo ../deployment/macos/install-vmguest.sh \
     /opt/reactorcide/images/macos-26-bootstrap ./vmguest
   ```

For the full procedure, see
[Step 2 in VM Image Operations](./vm-images.md#step-2-create-and-publish-a-bootstrap-image).

### Windows

Use a Windows 11 Pro, Enterprise, or Education host with Hyper-V, or Windows
Server. Use an elevated PowerShell session.

1. Build the guest daemon:

   ```sh
   cd coordinator_api
   GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -o vmguest.exe ./cmd/vmguest
   ```

2. Copy `deployment/windows` and `vmguest.exe` to the host.
3. Download the Windows ISO and build the base image:

   ```powershell
   .\download-windows-evaluation.ps1
   .\build-windows-image.ps1 `
     -IsoPath C:\ProgramData\Reactorcide\downloads\windows-11-enterprise-evaluation-25h2-en-us.iso `
     -IsoSha256 EXPECTED_64_CHARACTER_SHA256 `
     -ImageName 'Windows 11 Enterprise Evaluation' `
     -GuestDaemon .\vmguest.exe `
     -OutputDirectory C:\ProgramData\Reactorcide\vm-images\win11-base
   ```

The build takes approximately 10 minutes. For the full procedure, see
[Windows VM Image Build](./windows-vm-image-build.md).

Do a test of the base image before you publish it:

```powershell
.\vmsmoke.exe -bundle C:\ProgramData\Reactorcide\vm-images\win11-base -user reactorcide
```

The output must end with `vmsmoke: OK`.

## Step 2: Put the base image in a store

Select one store. You can use the two stores for the same image.

### OCI registry

A public registry needs no login. For a private registry, give the password
or token on standard input. Do not put it in a command argument:

```sh
registry-password-command | reactorcide vm-image registry login \
  --username ci-user --password-stdin registry.example.com
```

Publish the bundle:

```sh
reactorcide vm-image publish \
  /opt/reactorcide/images/macos-26-bootstrap \
  registry.example.com/team/vm/macos-26:base
```

### S3

The S3 store keeps an OCI image layout under the key prefix:

```text
s3://bucket/prefix/name/oci-layout
s3://bucket/prefix/name/index.json
s3://bucket/prefix/name/blobs/sha256/HEX
```

`index.json` records each tag in the `org.opencontainers.image.ref.name`
annotation.

Reactorcide gets the S3 credentials from the standard AWS chain: the
`AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` environment variables, the
shared AWS files, AWS SSO, or an instance or task role. Reactorcide does not
print credential values. Do not type a secret in a command argument. Put the
value in the environment from a file or a secret manager:

```sh
AWS_ACCESS_KEY_ID="$(cat ~/.config/reactorcide/s3-key-id)" \
AWS_SECRET_ACCESS_KEY="$(cat ~/.config/reactorcide/s3-secret)" \
  reactorcide vm-image publish \
    /opt/reactorcide/images/macos-26-bootstrap \
    s3://images/team/vm/macos-26:base
```

On Windows PowerShell, read the values from protected files in the same
command:

```powershell
$env:AWS_ACCESS_KEY_ID = (Get-Content -Raw C:\ProgramData\Reactorcide\secrets\s3-key-id).Trim()
$env:AWS_SECRET_ACCESS_KEY = (Get-Content -Raw C:\ProgramData\Reactorcide\secrets\s3-secret).Trim()
& 'C:\Program Files\Reactorcide\reactorcide.exe' vm-image publish `
  C:\ProgramData\Reactorcide\vm-images\win11-base `
  s3://images/team/vm/windows-11:base
Remove-Item Env:AWS_ACCESS_KEY_ID, Env:AWS_SECRET_ACCESS_KEY
```

For an S3-compatible service (MinIO, SeaweedFS, Ceph), set the endpoint.
Reactorcide then uses path-style addresses:

```sh
--s3-endpoint https://s3.internal.example.com
```

You can also set `REACTORCIDE_VM_S3_ENDPOINT`. If it is not set, Reactorcide
uses `REACTORCIDE_S3_ENDPOINT`. Use `--s3-region` or `AWS_REGION` for the
region. The default region is `us-east-1`.

Large images use S3 multipart upload. Reactorcide checks the size and the
digest of each blob during the upload. If the check fails, Reactorcide stops
the upload, and S3 does not keep the blob.

The publish command prints the pinned reference, for example:

```text
s3://images/team/vm/windows-11:base@sha256:DIGEST
```

Record this reference. Use it as the base of derived builds.

## Step 3: Build a derived image

A derived image is a base image plus your tools. The build boots the base in
a VM with no network adapter. Provisioning scripts get network access through
the guest daemon tunnel, like a job. To send this traffic through an egress
proxy, set `REACTORCIDE_VM_EGRESS_PROXY` on the build host.

Write each provisioning script so that it:

- Shows no prompt.
- Returns a nonzero exit code when it fails.
- Verifies the checksum of each download.
- Does not keep credentials in the image.
- Does not restart or stop the guest.

### macOS

Scripts run with `/bin/zsh -c` as the guest account:

```sh
reactorcide vm-image build macos \
  --from s3://images/team/vm/macos-26:base@sha256:DIGEST \
  --output /opt/reactorcide/images/macos-26-xcode \
  --provision ./images/install-xcode.sh \
  --publish s3://images/team/vm/macos-26-xcode:26.4
```

See [Step 4 in VM Image Operations](./vm-images.md#step-4-build-and-publish-a-derived-toolchain-image).

### Windows

Scripts are PowerShell files. They run with Windows PowerShell 5.1 as the
guest account (`--guest-user`, default `reactorcide`). When the account is an
administrator, the script gets the elevated token. Run the command in an
elevated PowerShell session on the Hyper-V host:

```powershell
$env:REACTORCIDE_VM_SCRATCH_DIR = 'C:\ProgramData\Reactorcide\vm-scratch'
& 'C:\Program Files\Reactorcide\reactorcide.exe' vm-image build windows `
  --from C:\ProgramData\Reactorcide\vm-images\win11-base `
  --output C:\ProgramData\Reactorcide\vm-images\win11-git `
  --provision .\provision\install-mingit.ps1 `
  --publish s3://images/team/vm/windows-11-git:1
```

`--from` accepts a local bundle directory, an OCI reference, or an `s3://`
reference. A reference is pulled into the local cache first.

For the command details, see
[Build a derived Windows image](./windows-vm-image-build.md#build-a-derived-windows-image).

This example script installs MinGit and adds it to the machine `PATH`:

```powershell
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'
$release = Invoke-RestMethod -UseBasicParsing `
  -Uri 'https://api.github.com/repos/git-for-windows/git/releases/latest'
$asset = $release.assets |
  Where-Object { $_.name -match '^MinGit-[0-9.]+-64-bit\.zip$' } |
  Select-Object -First 1
$zip = Join-Path $env:TEMP 'mingit.zip'
Invoke-WebRequest -UseBasicParsing -Uri $asset.browser_download_url -OutFile $zip
$actual = (Get-FileHash -Algorithm SHA256 -LiteralPath $zip).Hash.ToLowerInvariant()
if ($actual -ne ($asset.digest -replace '^sha256:', '')) { throw 'MinGit checksum mismatch.' }
Expand-Archive -Path $zip -DestinationPath C:\tools\mingit -Force
Remove-Item -LiteralPath $zip -Force
$path = [Environment]::GetEnvironmentVariable('Path', 'Machine')
[Environment]::SetEnvironmentVariable('Path', $path.TrimEnd(';') + ';C:\tools\mingit\cmd', 'Machine')
```

For a production image, pin the tool version and its SHA-256 value in the
script. Do not use `latest`.

## Step 4: Publish the derived image

Use `--publish` on the build command, or publish the bundle after a test:

```sh
reactorcide vm-image publish /opt/reactorcide/images/macos-26-xcode \
  registry.example.com/team/vm/macos-26-xcode:26.4
```

Pull an image to make sure that the store gives the correct content:

```sh
reactorcide vm-image pull s3://images/team/vm/windows-11-git:1@sha256:DIGEST
```

The pull verifies each blob digest before it extracts the bundle.

## Step 5: Set an alias

A job names an image by an alias. The coordinator changes the alias to the
pinned reference when it creates the job. Only workers that have the image
get the job.

Create the alias in the organization scope. Give the platform of the image
(`--os`, `--arch`). The commands use your API token (`--token` or
`REACTORCIDE_API_TOKEN`) and the coordinator URL (`--api-url` or
`REACTORCIDE_API_URL`):

```sh
reactorcide images alias set --org acme --os windows --arch amd64 \
  windows-git s3://images/team/vm/windows-11-git:1@sha256:DIGEST
```

Add `--project PROJECT_ID` to make a project alias. A project alias replaces
the organization alias with the same name for the jobs of that project.

Then use the alias in the job. The job must have the same `os` as the alias
(and the same `arch` when the job sets one). If it does not, the coordinator
refuses the job when it creates it:

```yaml
job:
  image: windows-git
  characteristics:
    os: windows
```

Workers that serve the alias download the image before they get jobs for
it. A worker is not ready until it has the images that it can be asked to
run. See the state of each serving worker:

```sh
reactorcide images rollout status --org acme windows-git
```

`workers list` also shows the image status of each worker that uses image
sets: `healthy`, `updating images`, or `image update failed` with the
reason.

To change the image, give the alias a new reference. New jobs use the old
reference until all workers that serve the alias have the new image:

```sh
reactorcide images alias set --org acme \
  windows-git s3://images/team/vm/windows-11-git:2@sha256:NEW_DIGEST
reactorcide images rollout status --org acme windows-git
```

Use `--force` only when you must use the new reference at once. Jobs then
wait until a worker has the new image. For all alias commands, rollouts, and
worker image states, see
[Image Aliases and Worker Image Sets](./image-aliases.md).
