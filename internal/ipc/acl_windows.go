//go:build windows

package ipc

import (
	"fmt"
	"sync"

	"golang.org/x/sys/windows"
)

var identity struct {
	sync.Once
	sddl string
	err  error
}

// ownerOnlySDDL grants full access to the current user and SYSTEM with
// inheritance blocked, so no other principal on the machine gets in. It is the
// same boundary the token file uses.
func ownerOnlySDDL() (string, error) {
	identity.Do(func() {
		user, err := windows.GetCurrentProcessToken().GetTokenUser()
		if err != nil {
			identity.err = fmt.Errorf("read the current user's SID: %w", err)
			return
		}
		identity.sddl = "D:P(A;;GA;;;" + user.User.Sid.String() + ")(A;;GA;;;SY)"
	})
	return identity.sddl, identity.err
}

// restrictToOwner rewrites the file's DACL so only the current user and SYSTEM
// can read it. Any same-user process holding the token can join the fleet, so
// the file must not inherit the directory's grants.
func restrictToOwner(path string) error {
	sddl, err := ownerOnlySDDL()
	if err != nil {
		return err
	}

	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return fmt.Errorf("build a security descriptor for %q: %w", path, err)
	}

	dacl, _, err := sd.DACL()
	if err != nil {
		return fmt.Errorf("read the DACL for %q: %w", path, err)
	}

	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil,
	); err != nil {
		return fmt.Errorf("restrict %q to its owner: %w", path, err)
	}
	return nil
}
