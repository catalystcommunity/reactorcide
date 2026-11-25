#!/bin/zsh
# Install the Reactorcide CI toolchains in a macOS guest image.
#
# Run as root with `reactorcide vm-image build macos --guest-user root`.
# The set is the Linux runnerbase image (git, Go, Rust, Python + uv,
# runnerlib, webi) plus the catalyst-tools bundle (Zig, JDK 17 + Gradle,
# .NET 8, Dart, Node, PHP). OCaml is installed per user by
# setup-runner-user.sh.
#
# The guest daemon starts jobs with a fixed PATH
# (/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin), so each
# tool is linked into /usr/local/bin. Tool trees are in /opt/catalyst-tools.
#
# Every download is pinned by version and SHA-256 (SHA-512 for .NET).
# To update a tool, change the URL and the checksum together.
set -eu
set -o pipefail

T=/opt/catalyst-tools
BIN=/usr/local/bin
DL=$(mktemp -d /private/tmp/ci-tools.XXXXXX)
trap 'rm -rf "$DL"' EXIT
mkdir -p "$T" "$BIN"

fetch() { # fetch URL ALGO SUM OUT
  curl -fsSL --retry 5 --retry-delay 3 -o "$4" "$1"
  echo "$3  $4" | shasum -a "$2" -c - >/dev/null || { echo "checksum mismatch: $1" >&2; exit 1; }
}
link() { # link TARGET... (into /usr/local/bin)
  for f in "$@"; do ln -sf "$f" "$BIN/$(basename "$f")"; done
}
say() { echo "== $*"; }

say Go 1.26.4
fetch https://go.dev/dl/go1.26.4.darwin-arm64.tar.gz 256 b62ad2b6d7d2464f12a5bcad7ff47f19d08325773b5efd21610e445a05a9bf53 "$DL/go.tgz"
rm -rf /usr/local/go && tar -xzf "$DL/go.tgz" -C /usr/local
link /usr/local/go/bin/go /usr/local/go/bin/gofmt

say Rust 1.100.0
fetch https://static.rust-lang.org/dist/rust-1.100.0-aarch64-apple-darwin.tar.xz 256 68ba729fe20df0f74309ef55ffbe518fc982177f327651b06112ef11e8e1de0d "$DL/rust.txz"
mkdir -p "$DL/rust" && tar -xJf "$DL/rust.txz" -C "$DL/rust" --strip-components=1
"$DL/rust/install.sh" --prefix=/usr/local --disable-ldconfig --components=rustc,cargo,rust-std-aarch64-apple-darwin,rustfmt-preview,clippy-preview >/dev/null

say Zig 0.14.1
fetch https://ziglang.org/download/0.14.1/zig-aarch64-macos-0.14.1.tar.xz 256 39f3dc5e79c22088ce878edc821dedb4ca5a1cd9f5ef915e9b3cc3053e8faefa "$DL/zig.txz"
rm -rf "$T/zig-0.14.1" && mkdir -p "$T/zig-0.14.1" && tar -xJf "$DL/zig.txz" -C "$T/zig-0.14.1" --strip-components=1
link "$T/zig-0.14.1/zig"

say Node 26.13.0
fetch https://nodejs.org/dist/v26.13.0/node-v26.13.0-darwin-arm64.tar.xz 256 6f5b3187d9df9fc007e881167905bc7d69ad26450ddc377c83d63256a2495b6e "$DL/node.txz"
rm -rf "$T/node-v26.13.0" && mkdir -p "$T/node-v26.13.0" && tar -xJf "$DL/node.txz" -C "$T/node-v26.13.0" --strip-components=1
link "$T/node-v26.13.0/bin/node" "$T/node-v26.13.0/bin/npm" "$T/node-v26.13.0/bin/npx" "$T/node-v26.13.0/bin/corepack"

say Temurin JDK 17.0.21.1+2
fetch 'https://github.com/adoptium/temurin17-binaries/releases/download/jdk-17.0.21.1%2B2/OpenJDK17U-jdk_aarch64_mac_hotspot_17.0.21.1_2.tar.gz' 256 2a88ccb044a8fb00957be93245da6357c11b55717511509d147cbcc5d93b2109 "$DL/jdk.tgz"
mkdir -p /Library/Java/JavaVirtualMachines
rm -rf /Library/Java/JavaVirtualMachines/temurin-17.jdk
mkdir -p "$DL/jdk" && tar -xzf "$DL/jdk.tgz" -C "$DL/jdk"
mv "$DL"/jdk/jdk-17* /Library/Java/JavaVirtualMachines/temurin-17.jdk

say Gradle 8.10.2
fetch https://services.gradle.org/distributions/gradle-8.10.2-bin.zip 256 31c55713e40233a8303827ceb42ca48a47267a0ad4bab9177123121e71524c26 "$DL/gradle.zip"
rm -rf "$T/gradle-8.10.2" && unzip -q "$DL/gradle.zip" -d "$T"
link "$T/gradle-8.10.2/bin/gradle"

say .NET SDK 8.0.428
fetch https://builds.dotnet.microsoft.com/dotnet/Sdk/8.0.428/dotnet-sdk-8.0.428-osx-arm64.tar.gz 512 c2930ce6363417165142e5610992df1430b1cf93f4b5ac31027da611a6bc089a73e7062eed0200be3e4a32db893a92c86fc27816a53521e5b041aafba0aa1215 "$DL/dotnet.tgz"
rm -rf /usr/local/share/dotnet && mkdir -p /usr/local/share/dotnet && tar -xzf "$DL/dotnet.tgz" -C /usr/local/share/dotnet
link /usr/local/share/dotnet/dotnet

say Dart 3.14.1
fetch https://storage.googleapis.com/dart-archive/channels/stable/release/3.14.1/sdk/dartsdk-macos-arm64-release.zip 256 b670699f921bf307c3f2754b56349863ee9b2878661bcd446b50b90296e11311 "$DL/dart.zip"
rm -rf "$T/dart-sdk" && unzip -q "$DL/dart.zip" -d "$T"
link "$T/dart-sdk/bin/dart"

say PHP 8.4.23 static CLI
fetch https://dl.static-php.dev/static-php-cli/common/php-8.4.23-cli-macos-aarch64.tar.gz 256 bba286e442796dbd420d778a016e2817b31d5036d11b3ba316d19a60de912cdc "$DL/php.tgz"
tar -xzf "$DL/php.tgz" -C "$BIN" php && chmod 0755 "$BIN/php"

say opam 2.6.1
fetch https://github.com/ocaml/opam/releases/download/2.6.1/opam-2.6.1-arm64-macos 256 3e7f67bda3ba0bf72f597917b17dc337ed5a1bd48224df8b747bb35ce05c569e "$BIN/opam"
chmod 0755 "$BIN/opam"

say uv 0.13.8 and Python 3.14
fetch https://github.com/astral-sh/uv/releases/download/0.13.8/uv-aarch64-apple-darwin.tar.gz 256 6b71028a6b904f2ae3340ef52e595512de807d06c0a20eaef2f692581b246558 "$DL/uv.tgz"
tar -xzf "$DL/uv.tgz" -C "$DL" && install -m 0755 "$DL"/uv-aarch64-apple-darwin/uv "$DL"/uv-aarch64-apple-darwin/uvx "$BIN/"
export UV_PYTHON_INSTALL_DIR="$T/python" UV_TOOL_DIR="$T/uv-tools" UV_TOOL_BIN_DIR="$BIN" UV_CACHE_DIR="$DL/uv-cache"
uv python install 3.14
py=$(uv python find 3.14)
link "$py"
ln -sf "$py" "$BIN/python3"
ln -sf "$py" "$BIN/python"

say runnerlib v0.13.0
uv tool install --python "$py" 'git+https://github.com/catalystcommunity/reactorcide@v0.13.0#subdirectory=runnerlib'

say webi
curl -fsSL https://webi.sh/webi -o "$DL/webi.sh"
HOME="$DL/webi-home" sh "$DL/webi.sh" >/dev/null
install -m 0755 "$DL/webi-home/.local/bin/webi" "$BIN/webi"

say OCaml 5.2.1
# The opam root belongs to the job account and is linked as its ~/.opam, so
# `opam` works in a job without OPAMROOT. The compiler is built from source
# with the Command Line Tools of the base image (about 10 minutes).
JOB_USER=${JOB_USER:-runner}
export OPAMROOT="$T/opam" OPAMYES=1 OPAMCOLOR=never
opam init --bare --disable-sandboxing --no-setup >/dev/null
opam switch create catalyst 5.2.1 >/dev/null
for f in "$T"/opam/catalyst/bin/*; do link "$f"; done
job_home=$(dscl . -read "/Users/$JOB_USER" NFSHomeDirectory | awk '{print $2}')
chown -R "$JOB_USER":staff "$T/opam"
rm -rf "$job_home/.opam" && ln -s "$T/opam" "$job_home/.opam" && chown -h "$JOB_USER":staff "$job_home/.opam"
unset OPAMROOT

# Read access for every account.
chmod -R a+rX "$T" /usr/local/go /usr/local/share/dotnet /Library/Java/JavaVirtualMachines/temurin-17.jdk

cat > "$T/VERSIONS" <<EOF
go 1.26.4
rust 1.100.0
zig 0.14.1
node 26.13.0
jdk temurin-17.0.21.1+2
gradle 8.10.2
dotnet-sdk 8.0.428
dart 3.14.1
php 8.4.23
opam 2.6.1
ocaml 5.2.1
uv 0.13.8
python 3.14
runnerlib v0.13.0
EOF
say done
