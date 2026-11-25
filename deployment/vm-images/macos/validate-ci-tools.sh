#!/bin/zsh
# Validate the CI toolchains as the job account. `vm-image build` runs every
# provisioning script as the one --guest-user (root for the install), so as
# root this script runs its own text again as JOB_USER (default runner).
# Each command must run with the fixed PATH of the guest daemon.
set -eu
set -o pipefail
if [ "$(id -u)" = 0 ]; then
  exec sudo -u "${JOB_USER:-runner}" -H env -i PATH="$PATH" /bin/zsh -c "$ZSH_EXECUTION_STRING"
fi
echo "user=$(id -un) PATH=$PATH"
# Capture the whole output first: `cmd | head` ends the command with SIGPIPE,
# and pipefail reports that as a failure.
check() { echo "-- $*"; local out; out=$("$@" 2>&1); head -n 2 <<< "$out"; }
check git --version
check go version
check rustc --version
check cargo --version
check zig version
check node --version
check npm --version
check java -version
check /usr/libexec/java_home
check gradle --version --quiet
check dotnet --version
check dart --version
check php --version
check opam --version
check ocaml -version
check uv --version
check python3 --version
check runnerlib --help
check webi --version
# A real compile for each language with a compiler, in a temp dir.
w=$(mktemp -d)
cd "$w"
printf 'package main\nimport "fmt"\nfunc main() { fmt.Println("go-ok") }\n' > main.go
go run main.go
printf 'fn main() { println!("rust-ok"); }\n' > m.rs
rustc -o m m.rs && ./m
printf 'const std = @import("std");\npub fn main() void { std.debug.print("zig-ok\\n", .{}); }\n' > m.zig
# Zig 0.14.1 (the catalyst-tools pin) cannot link a native executable on
# macOS 26: libSystem symbols such as _abort are undefined. Compile only.
zig build-obj m.zig && echo zig-obj-ok
printf 'let () = print_endline "ocaml-ok"\n' > m.ml
ocaml m.ml
cd / && rm -rf "$w"
cat /opt/catalyst-tools/VERSIONS
echo validate-ok
