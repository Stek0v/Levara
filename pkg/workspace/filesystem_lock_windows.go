//go:build windows

package workspace

import (
	"errors"
	"fmt"
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"unsafe"
)

func workspaceTryLock(f *os.File) (bool, error) {
	var overlapped windows.Overlapped
	err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlapped)
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return false, nil
	}
	return err == nil, err
}
func workspaceUnlock(f *os.File) {
	var overlapped windows.Overlapped
	_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &overlapped)
}

func workspaceLinkCount(f *os.File) (uint64, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(f.Fd()), &info); err != nil {
		return 0, err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return 0, fmt.Errorf("workspace reparse point forbidden")
	}
	return uint64(info.NumberOfLinks), nil
}
func workspaceNativeOpen(root *os.Root, name string, flags int, perm os.FileMode) (*os.File, error) {
	supported := os.O_WRONLY | os.O_RDWR | os.O_CREATE | os.O_EXCL | os.O_APPEND
	if flags & ^supported != 0 || flags&(os.O_WRONLY|os.O_RDWR) == os.O_WRONLY|os.O_RDWR || (flags&os.O_EXCL != 0 && flags&os.O_CREATE == 0) {
		return nil, fmt.Errorf("unsupported workspace native open flags")
	}
	access := uint32(windows.FILE_GENERIC_READ)
	if flags&os.O_RDWR != 0 {
		access = windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE
	}
	if flags&os.O_WRONLY != 0 {
		access = windows.FILE_GENERIC_WRITE
	}
	if flags&os.O_APPEND != 0 {
		if flags&os.O_WRONLY == 0 {
			return nil, fmt.Errorf("unsupported workspace append access")
		}
		// Append-only access preserves kernel EOF placement across processes.
		access = windows.FILE_APPEND_DATA | windows.FILE_READ_ATTRIBUTES | windows.SYNCHRONIZE
	}
	disposition := uint32(windows.FILE_OPEN)
	if flags&os.O_CREATE != 0 {
		disposition = windows.FILE_OPEN_IF
		if flags&os.O_EXCL != 0 {
			disposition = windows.FILE_CREATE
		}
	}
	attributes := uint32(windows.FILE_ATTRIBUTE_NORMAL)
	if perm.Perm()&0200 == 0 && flags&os.O_CREATE != 0 {
		attributes = windows.FILE_ATTRIBUTE_READONLY
	}
	parent, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	objectName, err := windows.NewNTUnicodeString(name)
	if err != nil {
		return nil, err
	}
	attrs := windows.OBJECT_ATTRIBUTES{RootDirectory: windows.Handle(parent.Fd()), ObjectName: objectName}
	attrs.Length = uint32(unsafe.Sizeof(attrs))
	var handle windows.Handle
	err = windows.NtCreateFile(&handle, access, &attrs, &windows.IO_STATUS_BLOCK{}, nil,
		attributes, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		disposition, windows.FILE_SYNCHRONOUS_IO_NONALERT|windows.FILE_OPEN_REPARSE_POINT|windows.FILE_NON_DIRECTORY_FILE, 0, 0)
	if err != nil {
		var status windows.NTStatus
		if errors.As(err, &status) {
			err = status.Errno()
		}
		return nil, &os.PathError{Op: "openat", Path: name, Err: err}
	}
	f := os.NewFile(uintptr(handle), filepath.Join(root.Name(), name))
	if f == nil {
		windows.CloseHandle(handle)
		return nil, fmt.Errorf("invalid workspace descriptor")
	}
	return f, nil
}
