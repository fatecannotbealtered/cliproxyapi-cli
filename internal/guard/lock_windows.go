//go:build windows

package guard

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

const (
	lockFileFailImmediately = 0x00000001
	lockFileExclusiveLock   = 0x00000002
	errorLockViolation      = syscall.Errno(33)
	lockByteOffset          = uint32(0xffffffff)
)

var (
	kernel32         = syscall.NewLazyDLL("kernel32.dll")
	procLockFileEx   = kernel32.NewProc("LockFileEx")
	procUnlockFileEx = kernel32.NewProc("UnlockFileEx")
)

func tryLockFile(file *os.File) error {
	overlapped := syscall.Overlapped{Offset: lockByteOffset}
	result, _, callErr := procLockFileEx.Call(
		file.Fd(),
		lockFileFailImmediately|lockFileExclusiveLock,
		0,
		1,
		0,
		uintptr(unsafe.Pointer(&overlapped)),
	)
	if result != 0 {
		return nil
	}
	if errors.Is(callErr, errorLockViolation) {
		return ErrLockHeld
	}
	return windowsCallError("LockFileEx", callErr)
}

func unlockFile(file *os.File) error {
	overlapped := syscall.Overlapped{Offset: lockByteOffset}
	result, _, callErr := procUnlockFileEx.Call(
		file.Fd(),
		0,
		1,
		0,
		uintptr(unsafe.Pointer(&overlapped)),
	)
	if result != 0 {
		return nil
	}
	return windowsCallError("UnlockFileEx", callErr)
}

func windowsCallError(name string, err error) error {
	if errno, ok := err.(syscall.Errno); ok && errno == 0 {
		return fmt.Errorf("%s failed", name)
	}
	return err
}
