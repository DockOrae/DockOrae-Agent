//go:build linux

package filemanager

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func preserveFileExtendedAttributes(target, source *os.File) error {
	size, err := unix.Flistxattr(int(source.Fd()), nil)
	if errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EOPNOTSUPP) {
		return nil
	}
	if err != nil || size == 0 {
		return err
	}
	names := make([]byte, size)
	count, err := unix.Flistxattr(int(source.Fd()), names)
	if err != nil {
		return err
	}
	for start := 0; start < count; {
		end := start
		for end < count && names[end] != 0 {
			end++
		}
		if end == start {
			start++
			continue
		}
		name := string(names[start:end])
		valueSize, getErr := unix.Fgetxattr(int(source.Fd()), name, nil)
		if getErr != nil {
			return getErr
		}
		value := make([]byte, valueSize)
		if valueSize > 0 {
			if _, getErr = unix.Fgetxattr(int(source.Fd()), name, value); getErr != nil {
				return getErr
			}
		}
		if setErr := unix.Fsetxattr(int(target.Fd()), name, value, 0); setErr != nil {
			return setErr
		}
		start = end + 1
	}
	return nil
}
