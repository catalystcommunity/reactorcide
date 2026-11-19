//go:build windows

package vmguest

import (
	"bytes"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// A Windows job VM has no network adapter, so Windows thinks it is offline.
// CryptoAPI then refuses to fetch CRLs and OCSP responses (ERROR_NOT_CONNECTED)
// before it reads the proxy settings, and schannel programs (git, curl.exe)
// fail with CRYPT_E_REVOCATION_OFFLINE. A Microsoft loopback adapter with an
// address and no gateway makes Windows report a connected network. It
// connects to nothing: traffic that does not use the proxy still fails.

// guidDevClassNet is GUID_DEVCLASS_NET, the setup class of netloop.inf.
var guidDevClassNet = windows.GUID{Data1: 0x4d36e972, Data2: 0xe325, Data3: 0x11ce, Data4: [8]byte{0xbf, 0xc1, 0x08, 0x00, 0x2b, 0xe1, 0x03, 0x18}}

var procUpdateDriverForPlugAndPlayDevices = windows.NewLazySystemDLL("newdev.dll").NewProc("UpdateDriverForPlugAndPlayDevicesW")

// ensureOfflineLoopback makes sure that the guest has the loopback adapter
// with loopbackAddress. It does nothing when the adapter is already there.
func ensureOfflineLoopback() error {
	present, err := loopbackPresent()
	if err != nil {
		return err
	}
	if !present {
		if err := installLoopback(); err != nil {
			return err
		}
	}
	return runPowerShell(loopbackAddressScript())
}

// loopbackPresent reports whether a network device with loopbackHardwareID
// exists.
func loopbackPresent() (bool, error) {
	set, err := windows.SetupDiGetClassDevsEx(&guidDevClassNet, "", 0, 0, 0, "")
	if err != nil {
		return false, fmt.Errorf("vmguest: list network devices: %w", err)
	}
	defer set.Close()
	for i := 0; ; i++ {
		data, err := set.EnumDeviceInfo(i)
		if err == windows.ERROR_NO_MORE_ITEMS {
			return false, nil
		}
		if err != nil {
			continue
		}
		value, err := set.DeviceRegistryProperty(data, windows.SPDRP_HARDWAREID)
		if err != nil {
			continue
		}
		ids, _ := value.([]string)
		for _, id := range ids {
			if strings.EqualFold(id, loopbackHardwareID) {
				return true, nil
			}
		}
	}
}

// installLoopback creates a root-enumerated loopback device and installs
// the inbox netloop.inf driver on it (what "devcon install netloop.inf
// *MSLOOP" does).
func installLoopback() error {
	set, err := windows.SetupDiCreateDeviceInfoListEx(&guidDevClassNet, 0, "")
	if err != nil {
		return fmt.Errorf("vmguest: loopback adapter: create device list: %w", err)
	}
	defer set.Close()
	data, err := set.CreateDeviceInfo("Net", &guidDevClassNet, "", 0, windows.DICD_GENERATE_ID)
	if err != nil {
		return fmt.Errorf("vmguest: loopback adapter: create device: %w", err)
	}
	ids, err := windows.UTF16FromString(loopbackHardwareID)
	if err != nil {
		return err
	}
	ids = append(ids, 0) // REG_MULTI_SZ ends with two NULs.
	buf := unsafe.Slice((*byte)(unsafe.Pointer(&ids[0])), len(ids)*2)
	if err := set.SetDeviceRegistryProperty(data, windows.SPDRP_HARDWAREID, buf); err != nil {
		return fmt.Errorf("vmguest: loopback adapter: set hardware ID: %w", err)
	}
	if err := set.CallClassInstaller(windows.DIF_REGISTERDEVICE, data); err != nil {
		return fmt.Errorf("vmguest: loopback adapter: register device: %w", err)
	}
	winDir, err := windows.GetWindowsDirectory()
	if err != nil {
		return err
	}
	inf, err := windows.UTF16PtrFromString(filepath.Join(winDir, loopbackINF))
	if err != nil {
		return err
	}
	hwid, err := windows.UTF16PtrFromString(loopbackHardwareID)
	if err != nil {
		return err
	}
	var reboot int32
	const installFlagForce = 0x1
	r, _, callErr := procUpdateDriverForPlugAndPlayDevices.Call(0, uintptr(unsafe.Pointer(hwid)), uintptr(unsafe.Pointer(inf)), installFlagForce, uintptr(unsafe.Pointer(&reboot)))
	if r == 0 {
		return fmt.Errorf("vmguest: loopback adapter: install driver: %w", callErr)
	}
	return nil
}

func runPowerShell(script string) error {
	cmd := exec.Command("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%w: %s", err, strings.TrimSpace(out.String()))
	}
	return nil
}
