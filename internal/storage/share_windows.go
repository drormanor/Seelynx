package storage

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"
)

var (
	mpr                   = syscall.NewLazyDLL("mpr.dll")
	procWNetAddConnection = mpr.NewProc("WNetAddConnection2W")
)

// netResource is Windows' NETRESOURCEW.
type netResource struct {
	Scope, Type, DisplayType, Usage          uint32
	LocalName, RemoteName, Comment, Provider *uint16
}

const resourceTypeDisk = 1

// Windows errors worth explaining.
const (
	errAccessDenied       = 5
	errBadNetPath         = 53
	errBadNetName         = 67
	errInvalidPassword    = 86
	errCredentialConflict = 1219
	errLogonFailure       = 1326
	errAccountRestriction = 1327
	errNoNetwork          = 1222
)

// connectShare makes a connection without a drive letter, as `net use \\nas\video`
// would. It lasts until Seelynx stops, and isn't saved for the next sign-in to Windows.
func connectShare(path, user, password string) error {
	root := shareRoot(path)
	if root == "" {
		return errors.New(`Enter a share path like \\nas\video.`)
	}
	remote, err := syscall.UTF16PtrFromString(root)
	if err != nil {
		return err
	}
	u, err := syscall.UTF16PtrFromString(user)
	if err != nil {
		return err
	}
	p, err := syscall.UTF16PtrFromString(password)
	if err != nil {
		return err
	}
	nr := netResource{Type: resourceTypeDisk, RemoteName: remote}
	r, _, _ := procWNetAddConnection.Call(uintptr(unsafe.Pointer(&nr)), uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(u)), 0)
	switch r {
	case 0:
		return nil
	case errLogonFailure, errInvalidPassword:
		return errors.New("The share refused that username or password.")
	case errAccountRestriction:
		return errors.New("The share refused this account. It may be disabled, or not allowed to sign in from here.")
	case errAccessDenied:
		return errors.New("The share refused access to this account. Give it permission to change the folder on the NAS.")
	case errBadNetPath:
		return fmt.Errorf("Seelynx can't find %s. Check the NAS name or address, and that it's on.", root)
	case errBadNetName:
		return fmt.Errorf("The NAS has no share by that name. Check the part after the NAS name in %s.", root)
	case errNoNetwork:
		return errors.New("This server isn't connected to a network.")
	case errCredentialConflict:
		return fmt.Errorf("Windows is already connected to this NAS with a different login. Windows allows only one login per NAS for each account. Use the same username as the other location on this NAS, or remove that connection with: net use %s /delete", root)
	}
	return fmt.Errorf("Windows couldn't connect to %s: %s", root, syscall.Errno(r).Error())
}
