//go:build !windows

package vmguest

import (
	"os"
	"os/exec"
	"os/user"
	"reflect"
	"testing"
)

func TestLimitGroups(t *testing.T) {
	many := make([]uint32, 0, 23)
	for g := uint32(0); g < 23; g++ {
		many = append(many, g)
	}
	got := limitGroups(20, many, 16)
	if len(got) != 16 || got[0] != 20 {
		t.Fatalf("limitGroups = %v, want 16 groups with the primary group first", got)
	}
	for _, g := range got[1:] {
		if g == 20 {
			t.Fatalf("limitGroups = %v repeats the primary group", got)
		}
	}
	if got := limitGroups(5, []uint32{5, 7}, 16); !reflect.DeepEqual(got, []uint32{5, 7}) {
		t.Fatalf("limitGroups short list = %v", got)
	}
}

// The daemon's own account runs without a credential switch. A root daemon
// that ran commands as root used to pass all of root's groups (23 on macOS)
// to setgroups, and fork/exec failed with EINVAL.
func TestLookupAccountOwnUserNeedsNoSwitch(t *testing.T) {
	me, err := user.Current()
	if err != nil {
		t.Skip(err)
	}
	a, err := lookupAccount(me.Username, false)
	if err != nil {
		t.Fatal(err)
	}
	if a.set || a.name != me.Username || a.home != me.HomeDir {
		t.Fatalf("lookupAccount(own user) = %+v, want no switch with name and home", a)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	configureCmd(cmd, a)
	if cmd.SysProcAttr.Credential != nil {
		t.Fatal("own account must not set a credential")
	}
	if err := cmd.Run(); err != nil {
		t.Fatalf("run as own account: %v", err)
	}
}
