#!/bin/sh
# Install the Reactorcide guest daemon (vmguest) into a stopped macOS VM
# bundle. Adapted from the Magi prepare-template.sh. Run it as root on the
# Mac host, with the VM stopped:
#
#   sudo deployment/macos/install-vmguest.sh BUNDLE_DIR VMGUEST_BINARY
#
# VMGUEST_BINARY is a darwin/arm64 build of cmd/vmguest:
#
#   cd coordinator_api
#   GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -o vmguest ./cmd/vmguest
#
# The script attaches BUNDLE_DIR/disk.img without mounting it, mounts the
# Data volume of that image only, and adds:
#   /usr/local/libexec/reactorcide-vmguest  (the daemon, root:wheel 0755)
#   /Library/LaunchDaemons/community.catalyst.reactorcide.vmguest.plist
#   /private/var/reactorcide/job (mode 1777) and the /etc/synthetic.conf
#   entry that links /job to it at boot
# launchd starts the daemon at boot, with no login. The script changes
# nothing else in the guest. Use it on a bootstrap bundle; derived images
# get updates from `reactorcide vm-image build macos --guest-daemon`.
set -eu
dir=${1:?usage: install-vmguest.sh BUNDLE_DIR VMGUEST_BINARY}
bin=${2:?usage: install-vmguest.sh BUNDLE_DIR VMGUEST_BINARY}
[ "$(id -u)" = 0 ] || { echo "run as root" >&2; exit 1; }
[ -f "$dir/disk.img" ] || { echo "no $dir/disk.img" >&2; exit 1; }
[ -f "$bin" ] || { echo "no $bin" >&2; exit 1; }
file "$bin" | grep -q 'Mach-O 64-bit executable arm64' || { echo "$bin is not a darwin/arm64 executable" >&2; exit 1; }

disk=$(diskutil image attach --noMount "$dir/disk.img" | awk 'NR==1{print $1}')
mnt=
cleanup() {
  if [ -n "$mnt" ]; then
    diskutil unmount "$mnt" >/dev/null 2>&1 || true
    rmdir "$mnt" 2>/dev/null || true
  fi
  hdiutil detach "$disk" >/dev/null 2>&1 || hdiutil detach -force "$disk" >/dev/null 2>&1 || true
}
trap cleanup EXIT
sleep 2
# The Data volume of the containers in this image only. Do not use a volume
# of the host: `diskutil mount` of a mounted host volume "succeeds" and the
# writes go to an empty directory.
data=
for c in $(diskutil list "$disk" | awk '/Apple_APFS /{for (i = 1; i < NF; i++) if ($i == "Container") print $(i + 1)}'); do
  v=$(diskutil apfs list "$c" | awk '/APFS Volume Disk \(Role\):.*\(Data\)/{print $(NF-1)}' | head -1)
  [ -n "$v" ] && data=$v
done
[ -n "$data" ] || { echo "no Data volume in $dir/disk.img" >&2; exit 1; }
mnt=$(cd "$(mktemp -d)" && pwd -P)
diskutil mount -mountPoint "$mnt" "$data" >/dev/null
mount | grep -q " on $mnt " || { echo "could not mount $data at $mnt" >&2; exit 1; }
# An attached image mounts with ownership off; launchd ignores a daemon file
# that is not root:wheel.
diskutil enableOwnership "$mnt" >/dev/null

mkdir -p "$mnt/usr/local/libexec" "$mnt/Library/LaunchDaemons"
cp "$bin" "$mnt/usr/local/libexec/reactorcide-vmguest.new"
mv -f "$mnt/usr/local/libexec/reactorcide-vmguest.new" "$mnt/usr/local/libexec/reactorcide-vmguest"
cat > "$mnt/Library/LaunchDaemons/community.catalyst.reactorcide.vmguest.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>community.catalyst.reactorcide.vmguest</string>
	<key>ProgramArguments</key><array><string>/usr/local/libexec/reactorcide-vmguest</string><string>serve</string></array>
	<key>EnvironmentVariables</key><dict><key>PATH</key><string>/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin</string></dict>
	<key>RunAtLoad</key><true/>
	<key>KeepAlive</key><true/>
	<key>StandardOutPath</key><string>/var/log/reactorcide-vmguest.log</string>
	<key>StandardErrorPath</key><string>/var/log/reactorcide-vmguest.log</string>
</dict>
</plist>
PLIST
# /job: the macOS system volume is read-only, so /job is a synthetic link
# that macOS makes at each boot (see vmguest.MacJobDirSetupScript).
mkdir -p "$mnt/private/var/reactorcide/job"
chown root:wheel "$mnt/private/var/reactorcide" "$mnt/private/var/reactorcide/job"
chmod 1777 "$mnt/private/var/reactorcide/job"
if ! grep -qE '^job[[:space:]]' "$mnt/private/etc/synthetic.conf" 2>/dev/null; then
  printf 'job\tprivate/var/reactorcide/job\n' >> "$mnt/private/etc/synthetic.conf"
fi
chown root:wheel "$mnt/private/etc/synthetic.conf"
chmod 644 "$mnt/private/etc/synthetic.conf"
chown root:wheel "$mnt/usr/local/libexec/reactorcide-vmguest" "$mnt/Library/LaunchDaemons/community.catalyst.reactorcide.vmguest.plist"
chmod 755 "$mnt/usr/local/libexec/reactorcide-vmguest"
chmod 644 "$mnt/Library/LaunchDaemons/community.catalyst.reactorcide.vmguest.plist"
echo "installed vmguest in $dir"
