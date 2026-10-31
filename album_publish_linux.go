package wrtag

import (
	"errors"
	"golang.org/x/sys/unix"
)

func init() {
	publishAlbumDirectory = func(src, dest string) error {
		err := unix.Renameat2(unix.AT_FDCWD, src, unix.AT_FDCWD, dest, unix.RENAME_NOREPLACE)
		if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.EOPNOTSUPP) {
			return errDirectoryPublicationUnsupported
		}
		return err
	}
}
