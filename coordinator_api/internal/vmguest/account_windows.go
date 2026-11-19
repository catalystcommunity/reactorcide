//go:build windows

package vmguest

import (
	"errors"
	"fmt"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The guest daemon runs as LocalSystem. A job runs as a local guest account
// (default "reactorcide"), so that it has a normal user profile, HKCU,
// %USERPROFILE%, %APPDATA%, and %TEMP%, and not the LocalSystem ones.
//
// At the first connection for an account, the daemon:
//  1. creates the account if it does not exist (a member of Administrators
//     unless the job sets run_as.administrator to false), or else keeps its
//     groups;
//  2. sets a new random password that only the daemon knows;
//  3. logs the account on (interactive logon) and keeps the token. For an
//     administrator under UAC it uses the elevated linked token, as a CI
//     user on a hosted Windows runner has;
//  4. loads the user profile (this makes C:\Users\NAME on the first boot);
//  5. gives the account access to the daemon's window station and desktop.
//     A child process starts on the desktop of its parent, and a program that
//     loads user32.dll stops with 0xC0000142 when it has no access to it.
//
// The token, the profile, and the access stay until the daemon stops. The
// guest is a one-job VM.

var (
	modadvapi32 = windows.NewLazySystemDLL("advapi32.dll")
	moduserenv  = windows.NewLazySystemDLL("userenv.dll")
	modnetapi32 = windows.NewLazySystemDLL("netapi32.dll")
	moduser32   = windows.NewLazySystemDLL("user32.dll")

	procLogonUserW                = modadvapi32.NewProc("LogonUserW")
	procLoadUserProfileW          = moduserenv.NewProc("LoadUserProfileW")
	procNetUserAdd                = modnetapi32.NewProc("NetUserAdd")
	procNetUserSetInfo            = modnetapi32.NewProc("NetUserSetInfo")
	procNetLocalGroupAddMembers   = modnetapi32.NewProc("NetLocalGroupAddMembers")
	procGetProcessWindowStation   = moduser32.NewProc("GetProcessWindowStation")
	procGetUserObjectInformationW = moduser32.NewProc("GetUserObjectInformationW")
	procOpenWindowStationW        = moduser32.NewProc("OpenWindowStationW")
	procCloseWindowStation        = moduser32.NewProc("CloseWindowStation")
	procOpenDesktopW              = moduser32.NewProc("OpenDesktopW")
	procCloseDesktop              = moduser32.NewProc("CloseDesktop")
)

const (
	logon32LogonInteractive = 2
	logon32ProviderDefault  = 0

	userPrivUser       = 1
	ufScript           = 0x0001
	ufDontExpirePasswd = 0x10000

	nerrUserExists     = 2224
	nerrUserNotFound   = 2221
	errorMemberInAlias = 1378

	piNoUI = 1

	uoiName       = 2
	readControl   = 0x00020000
	writeDAC      = 0x00040000
	genericAll    = 0x10000000
	desktopAccess = readControl | writeDAC
)

type userInfo1 struct {
	Name        *uint16
	Password    *uint16
	PasswordAge uint32
	Priv        uint32
	HomeDir     *uint16
	Comment     *uint16
	Flags       uint32
	ScriptPath  *uint16
}

type userInfo1003 struct {
	Password *uint16
}

type localGroupMembersInfo0 struct {
	SID *windows.SID
}

type profileInfo struct {
	Size        uint32
	Flags       uint32
	UserName    *uint16
	ProfilePath *uint16
	DefaultPath *uint16
	ServerName  *uint16
	PolicyPath  *uint16
	Profile     windows.Handle
}

var (
	accountsMu sync.Mutex
	accounts   = map[string]account{}
)

// lookupAccount returns the logged-on guest account. An empty name means the
// daemon account (LocalSystem).
func lookupAccount(name string, createAdmin bool) (account, error) {
	if name == "" {
		return account{}, nil
	}
	if err := validWindowsAccountName(name); err != nil {
		return account{}, err
	}
	accountsMu.Lock()
	defer accountsMu.Unlock()
	if a, ok := accounts[name]; ok {
		return a, nil
	}
	a, err := logonAccount(name, createAdmin)
	if err != nil {
		return account{}, err
	}
	accounts[name] = a
	return a, nil
}

func logonAccount(name string, createAdmin bool) (account, error) {
	password, err := newWindowsPassword()
	if err != nil {
		return account{}, err
	}
	if err := ensureLocalUser(name, password, createAdmin); err != nil {
		return account{}, err
	}
	token, err := logonUser(name, password)
	if err != nil {
		return account{}, fmt.Errorf("vmguest: log on guest account %q: %w", name, err)
	}
	ok := false
	defer func() {
		if !ok {
			token.Close()
		}
	}()
	if !token.IsElevated() {
		// An administrator under UAC gets a filtered token. The daemon has
		// SeTcbPrivilege, so the linked token is a primary token.
		if linked, err := token.GetLinkedToken(); err == nil {
			if linked.IsElevated() {
				token.Close()
				token = linked
			} else {
				linked.Close()
			}
		}
	}
	user, err := token.GetTokenUser()
	if err != nil {
		return account{}, fmt.Errorf("vmguest: guest account %q SID: %w", name, err)
	}
	sid, err := user.User.Sid.Copy()
	if err != nil {
		return account{}, err
	}
	if err := loadUserProfile(token, name); err != nil {
		return account{}, fmt.Errorf("vmguest: load profile of %q: %w", name, err)
	}
	home, err := token.GetUserProfileDirectory()
	if err != nil {
		return account{}, fmt.Errorf("vmguest: profile directory of %q: %w", name, err)
	}
	env, err := tokenEnvironment(token)
	if err != nil {
		return account{}, fmt.Errorf("vmguest: environment of %q: %w", name, err)
	}
	if err := grantDesktopAccess(sid); err != nil {
		return account{}, fmt.Errorf("vmguest: desktop access for %q: %w", name, err)
	}
	ok = true
	return account{set: true, name: name, home: home, sid: sid, token: token, environ: withHome(env, home), admin: token.IsElevated()}, nil
}

// ensureLocalUser creates the account, or sets a new password on an
// existing one. A new account is a member of Administrators when createAdmin
// is true (the default): Windows build tools and installers often need
// elevation, and the guest is a disposable VM with no network adapter. An
// account that the image has keeps its groups.
func ensureLocalUser(name, password string, createAdmin bool) error {
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	p, err := windows.UTF16PtrFromString(password)
	if err != nil {
		return err
	}
	info := userInfo1003{Password: p}
	r, _, _ := procNetUserSetInfo.Call(0, uintptr(unsafe.Pointer(n)), 1003, uintptr(unsafe.Pointer(&info)), 0)
	switch r {
	case 0:
		return nil
	case nerrUserNotFound:
	default:
		return fmt.Errorf("vmguest: set password of %q: NET_API_STATUS %d", name, r)
	}
	add := userInfo1{Name: n, Password: p, Priv: userPrivUser, Flags: ufScript | ufDontExpirePasswd}
	r, _, _ = procNetUserAdd.Call(0, 1, uintptr(unsafe.Pointer(&add)), 0)
	if r != 0 && r != nerrUserExists {
		return fmt.Errorf("vmguest: create guest account %q: NET_API_STATUS %d", name, r)
	}
	if !createAdmin {
		return nil
	}
	return addToAdministrators(name)
}

func addToAdministrators(name string) error {
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return err
	}
	// The group name depends on the OS language.
	group, _, _, err := admins.LookupAccount("")
	if err != nil {
		return err
	}
	sid, _, _, err := windows.LookupSID("", name)
	if err != nil {
		return fmt.Errorf("vmguest: SID of %q: %w", name, err)
	}
	g, err := windows.UTF16PtrFromString(group)
	if err != nil {
		return err
	}
	member := localGroupMembersInfo0{SID: sid}
	r, _, _ := procNetLocalGroupAddMembers.Call(0, uintptr(unsafe.Pointer(g)), 0, uintptr(unsafe.Pointer(&member)), 1)
	if r != 0 && r != errorMemberInAlias {
		return fmt.Errorf("vmguest: add %q to %s: NET_API_STATUS %d", name, group, r)
	}
	return nil
}

func logonUser(name, password string) (windows.Token, error) {
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	dot, _ := windows.UTF16PtrFromString(".")
	p, err := windows.UTF16PtrFromString(password)
	if err != nil {
		return 0, err
	}
	var token windows.Token
	r, _, e := procLogonUserW.Call(uintptr(unsafe.Pointer(n)), uintptr(unsafe.Pointer(dot)), uintptr(unsafe.Pointer(p)),
		logon32LogonInteractive, logon32ProviderDefault, uintptr(unsafe.Pointer(&token)))
	if r == 0 {
		return 0, callErr(e)
	}
	return token, nil
}

func loadUserProfile(token windows.Token, name string) error {
	for _, priv := range []string{"SeBackupPrivilege", "SeRestorePrivilege"} {
		if err := enablePrivilege(priv); err != nil {
			return err
		}
	}
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	pi := profileInfo{Flags: piNoUI, UserName: n}
	pi.Size = uint32(unsafe.Sizeof(pi))
	r, _, e := procLoadUserProfileW.Call(uintptr(token), uintptr(unsafe.Pointer(&pi)))
	if r == 0 {
		return callErr(e)
	}
	// Keep the profile handle. The profile stays loaded until the VM stops.
	return nil
}

func enablePrivilege(name string) error {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, &token); err != nil {
		return err
	}
	defer token.Close()
	n, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	var luid windows.LUID
	if err := windows.LookupPrivilegeValue(nil, n, &luid); err != nil {
		return fmt.Errorf("vmguest: privilege %s: %w", name, err)
	}
	tp := windows.Tokenprivileges{PrivilegeCount: 1}
	tp.Privileges[0] = windows.LUIDAndAttributes{Luid: luid, Attributes: windows.SE_PRIVILEGE_ENABLED}
	if err := windows.AdjustTokenPrivileges(token, false, &tp, 0, nil, nil); err != nil {
		return fmt.Errorf("vmguest: enable %s: %w", name, err)
	}
	return nil
}

func tokenEnvironment(token windows.Token) ([]string, error) {
	var block *uint16
	if err := windows.CreateEnvironmentBlock(&block, token, false); err != nil {
		return nil, err
	}
	defer windows.DestroyEnvironmentBlock(block)
	// Find the end of the block: two NUL characters in sequence.
	var n int
	for p := unsafe.Pointer(block); ; n++ {
		if *(*uint16)(unsafe.Add(p, n*2)) == 0 && *(*uint16)(unsafe.Add(p, (n+1)*2)) == 0 {
			n += 2
			break
		}
	}
	return parseEnvBlock(unsafe.Slice(block, n)), nil
}

// grantDesktopAccess gives sid full access to the window station and the
// "Default" desktop of the daemon.
func grantDesktopAccess(sid *windows.SID) error {
	cur, _, e := procGetProcessWindowStation.Call()
	if cur == 0 {
		return callErr(e)
	}
	var buf [256]uint16
	var needed uint32
	r, _, e := procGetUserObjectInformationW.Call(cur, uoiName, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)*2), uintptr(unsafe.Pointer(&needed)))
	if r == 0 {
		return callErr(e)
	}
	ws, _, e := procOpenWindowStationW.Call(uintptr(unsafe.Pointer(&buf[0])), 0, desktopAccess)
	if ws == 0 {
		return fmt.Errorf("open window station %s: %w", windows.UTF16ToString(buf[:]), callErr(e))
	}
	defer procCloseWindowStation.Call(ws)
	if err := grantObject(windows.Handle(ws), sid); err != nil {
		return fmt.Errorf("window station: %w", err)
	}
	def, _ := windows.UTF16PtrFromString("Default")
	desk, _, e := procOpenDesktopW.Call(uintptr(unsafe.Pointer(def)), 0, 0, desktopAccess)
	if desk == 0 {
		return fmt.Errorf("open desktop: %w", callErr(e))
	}
	defer procCloseDesktop.Call(desk)
	if err := grantObject(windows.Handle(desk), sid); err != nil {
		return fmt.Errorf("desktop: %w", err)
	}
	return nil
}

func grantObject(h windows.Handle, sid *windows.SID) error {
	sd, err := windows.GetSecurityInfo(h, windows.SE_WINDOW_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	old, _, err := sd.DACL()
	if err != nil && !errors.Is(err, windows.ERROR_OBJECT_NOT_FOUND) {
		return err
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: genericAll,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.NO_INHERITANCE,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}}, old)
	if err != nil {
		return err
	}
	return windows.SetSecurityInfo(h, windows.SE_WINDOW_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

// grantFile gives sid full control of a file or directory. A directory
// passes the access to new children.
func grantFile(path string, sid *windows.SID, dir bool) error {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	old, _, err := sd.DACL()
	if err != nil && !errors.Is(err, windows.ERROR_OBJECT_NOT_FOUND) {
		return err
	}
	inherit := uint32(windows.NO_INHERITANCE)
	if dir {
		inherit = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       inherit,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}}, old)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

func callErr(e error) error {
	if errno, ok := e.(syscall.Errno); ok && errno != 0 {
		return errno
	}
	return syscall.EINVAL
}
