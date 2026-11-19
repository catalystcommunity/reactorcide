package vmguest

import "strings"

// Installed locations of the guest daemon.
const (
	// MacDaemonPath is the daemon binary in a macOS guest.
	MacDaemonPath = "/usr/local/libexec/reactorcide-vmguest"
	// MacLaunchdLabel is the launchd label of the daemon.
	MacLaunchdLabel = "community.catalyst.reactorcide.vmguest"
	// MacLaunchdPlist is the launchd daemon file.
	MacLaunchdPlist = "/Library/LaunchDaemons/" + MacLaunchdLabel + ".plist"
	// MacDaemonLog is the daemon log in a macOS guest.
	MacDaemonLog = "/var/log/reactorcide-vmguest.log"
	// WindowsDaemonPath is the daemon binary in a Windows guest.
	WindowsDaemonPath = `C:\Program Files\Reactorcide\vmguest.exe`
	// WindowsServiceName is the Windows service of the daemon.
	WindowsServiceName = "reactorcide-vmguest"
)

// MacJobDirTarget is the writable directory behind /job in a macOS guest.
// The system volume of macOS is read-only, so /job is a synthetic link
// (/etc/synthetic.conf) that macOS makes at each boot.
const MacJobDirTarget = "/private/var/reactorcide/job"

// MacJobDirSetupScript makes /job in a macOS guest. It runs as root in the
// guest (vmguest install, and each macOS image build). It is idempotent. The
// link appears at the next boot; apfs.util -t also makes it at once when
// macOS permits it. The directory has mode 1777 (like /tmp): the daemon
// gives each uploaded tree to the guest account, and the job can make new
// files in /job.
const MacJobDirSetupScript = `set -e
mkdir -p ` + MacJobDirTarget + `
chmod 1777 ` + MacJobDirTarget + `
if ! grep -qE '^job[[:space:]]' /etc/synthetic.conf 2>/dev/null; then
  printf 'job\tprivate/var/reactorcide/job\n' >> /etc/synthetic.conf
fi
chown root:wheel /etc/synthetic.conf
chmod 644 /etc/synthetic.conf
if [ ! -e /job ]; then
  /System/Library/Filesystems/apfs.fs/Contents/Resources/apfs.util -t >/dev/null 2>&1 || true
fi
`

// macGuestPath is the PATH of commands in a macOS guest. launchd gives a
// daemon only the system directories.
const macGuestPath = "/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin:/usr/sbin:/sbin"

// LaunchdPlist returns the launchd daemon definition for the binary at
// program. launchd starts it at boot (with no login) and restarts it.
func LaunchdPlist(program string) string {
	esc := func(s string) string {
		return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(s)
	}
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>` + MacLaunchdLabel + `</string>
	<key>ProgramArguments</key><array><string>` + esc(program) + `</string><string>serve</string></array>
	<key>EnvironmentVariables</key><dict><key>PATH</key><string>` + macGuestPath + `</string></dict>
	<key>RunAtLoad</key><true/>
	<key>KeepAlive</key><true/>
	<key>StandardOutPath</key><string>` + MacDaemonLog + `</string>
	<key>StandardErrorPath</key><string>` + MacDaemonLog + `</string>
</dict>
</plist>
`
}
