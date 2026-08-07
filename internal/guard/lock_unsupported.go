//go:build !windows && !linux && !darwin

package guard

import (
	"errors"
	"os"
)

func tryLockFile(*os.File) error {
	return errors.New("guard file locking is unsupported on this platform")
}

func unlockFile(*os.File) error {
	return nil
}
