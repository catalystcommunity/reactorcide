# CI VM Images

This directory has the provisioning scripts of the macOS and Windows CI
images. The images are derived images: each build starts from a base image
that has the guest daemon, and adds the CI toolchains.

The images are private. They are in
`containers.catalystsquad.com/private/reactorcide/vm/`. The licenses of
Windows Evaluation and macOS do not let us publish them.

## Tool set

The tool set is the Linux `runnerbase` image plus the catalyst-tools bundle
of `csilgen/tools/install-transport-toolchains.sh`. Use the same versions in
all three places.

| Tool | Version | macOS | Windows |
|---|---|---|---|
| Git | system / 2.56.1 | Command Line Tools | Git for Windows (with `bash`) |
| C toolchain | system / GCC 16.2.0 | Command Line Tools (clang) | WinLibs MinGW-w64 (msvcrt) |
| Go | 1.26.4 | yes | yes |
| Rust | 1.100.0 | `aarch64-apple-darwin` | `x86_64-pc-windows-gnu` |
| Python + uv | 3.14 + 0.13.8 | yes | yes |
| runnerlib | v0.13.0 | yes | yes |
| Zig | 0.14.1 | compile only (see below) | yes |
| JDK + Gradle | Temurin 17 + 8.10.2 | yes | yes |
| .NET SDK | 8.0.428 | yes | yes |
| Dart | 3.14.1 | yes | yes |
| Node | 26.13.0 | yes | yes |
| PHP | 8.4 | static CLI 8.4.23 | NTS 8.4.28 |
| OCaml + opam | 5.2.1 + 2.6.1 | yes | no |
| webi | latest | yes | no |

Each image has the list in `/opt/catalyst-tools/VERSIONS` (macOS) or
`C:\catalyst-tools\VERSIONS` (Windows).

## Paths

- macOS: the guest daemon starts each job with a fixed `PATH`
  (`/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin`). The
  script installs each tool tree in `/opt/catalyst-tools` and links the
  commands into `/usr/local/bin`. The JDK is in
  `/Library/Java/JavaVirtualMachines`. The opam root is
  `/opt/catalyst-tools/opam`, which is linked as `~runner/.opam`.
- Windows: the script installs the tools for all accounts. It adds them to
  the machine `PATH` and sets `JAVA_HOME` and `DOTNET_ROOT`. A job account
  gets these values when it logs on.

## Build an image

Each download in the scripts has a pinned version and a SHA-256 checksum
(SHA-512 for .NET). The VC++ runtime has no published checksum, so the
Windows script checks its Microsoft Authenticode signature. To change a tool,
change its URL and its checksum together.

The build host needs a registry credential file with write access to the
private repository. Remove the file from the host after the publish.

### macOS

The build runs every provisioning script as one `--guest-user`. The install
needs `root`. The validation script starts its text again as the job account
(`runner`).

The provisioning scripts run under the guest daemon that is already running.
`--guest-daemon` writes a new daemon to the disk, but this daemon starts only
at the next boot. If the base image has an old daemon, build in two passes:

```sh
# Pass 1: install the new guest daemon only.
printf '#!/bin/zsh\necho daemon-updated\n' > noop.sh
reactorcide vm-image build macos --from-local --from BASE_BUNDLE \
  --output IMAGES/macos-26-base-vmguest \
  --guest-daemon ./vmguest --guest-user runner --provision noop.sh

# Pass 2: install and validate the tools, then publish.
reactorcide vm-image build macos --from-local \
  --from IMAGES/macos-26-base-vmguest \
  --output IMAGES/macos-26-ci-YYYYMMDD \
  --guest-user root \
  --provision install-ci-tools.sh --provision validate-ci-tools.sh \
  --cpus 8 --memory 12Gi \
  --auth-file PUSH_AUTH_FILE \
  --publish containers.catalystsquad.com/private/reactorcide/vm/macos-26-ci:YYYYMMDD
```

### Windows

```powershell
reactorcide.exe vm-image build windows `
  --from C:\ProgramData\Reactorcide\vm-images\BASE `
  --output C:\ProgramData\Reactorcide\vm-images\windows-11-ci-YYYYMMDD `
  --guest-daemon .\vmguest.exe --guest-user reactorcide `
  --provision install-ci-tools.ps1 --provision validate-ci-tools.ps1 `
  --cpus 8 --memory 16Gi `
  --auth-file PUSH_AUTH_FILE `
  --publish containers.catalystsquad.com/private/reactorcide/vm/windows-11-ci:YYYYMMDD
```

## Known limits

- Zig 0.14.1 cannot link a native executable on macOS 26. The linker cannot
  find libSystem symbols such as `_abort`. Zig can compile objects and
  cross-compile. A later Zig release is necessary for native macOS links.
- The Visual Studio Build Tools bootstrapper cannot download in a job VM.
  BITS and WinINET in the bootstrapper do not use the guest egress proxy
  (WinINET error 12007). For this reason, Windows Rust uses the GNU target
  and the WinLibs GCC, not MSVC.
- In PowerShell 5.1, do not pipe a native program to `Select-Object -First`.
  The pipeline stops the program early, and the exit code is -1. With
  `$ErrorActionPreference = 'Stop'`, redirected stderr output is a
  terminating error. The scripts collect the output first and use
  `Continue` for native programs.
- In zsh with `pipefail`, `command | head` can fail with SIGPIPE. Collect the
  output first.
