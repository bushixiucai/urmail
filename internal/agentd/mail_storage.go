package agentd

import (
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// prepareMailStorage provisions only the parents needed by Dovecot's managed
// maildir layout. Existing mail, certificates and backups are never reowned.
func (m *ApplyManager) prepareMailStorage() error {
	if m.cfg.DNSSecondaryOnly {
		return nil
	}
	uid, gid := os.Geteuid(), os.Getegid()
	if m.targetRoot == "" {
		account, err := user.Lookup("mail")
		if err != nil {
			return fmt.Errorf("mail storage user: %w", err)
		}
		group, err := user.LookupGroup("mail")
		if err != nil {
			return fmt.Errorf("mail storage group: %w", err)
		}
		uid, err = strconv.Atoi(account.Uid)
		if err != nil {
			return err
		}
		gid, err = strconv.Atoi(group.Gid)
		if err != nil {
			return err
		}
		if uid <= 0 || gid <= 0 {
			return fmt.Errorf("mail storage requires the unprivileged mail identity")
		}
	}
	return ensureMailStorageTree(m.target(m.cfg.StorageRoot), uid, gid)
}

// Directory descriptors prevent a symlink substitution from redirecting a
// privileged mkdir/chown. Only directories created by this invocation receive
// ownership/mode changes; an incompatible existing tree is refused in place.
func ensureMailStorageTree(root string, uid, gid int) error {
	cleaned := filepath.Clean(root)
	if !filepath.IsAbs(root) || cleaned == "/" || cleaned != strings.TrimRight(root, "/") || uid < 0 || gid < 0 {
		return fmt.Errorf("%w: invalid mail storage root or identity", ErrRejected)
	}
	parts := append(strings.Split(strings.TrimPrefix(cleaned, "/"), "/"), "mail", "mailboxes")
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(fd) }()
	for i, name := range parts {
		child, openErr := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		created := false
		managed := i >= len(parts)-2
		if errors.Is(openErr, unix.ENOENT) {
			mode := uint32(0755)
			if managed {
				mode = 0750
			}
			if err = unix.Mkdirat(fd, name, mode); err != nil && !errors.Is(err, unix.EEXIST) {
				return fmt.Errorf("%w: create mail storage directory: %w", ErrFailed, err)
			}
			created = err == nil
			child, openErr = unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		}
		if openErr != nil {
			return fmt.Errorf("%w: mail storage component must be a real directory: %w", ErrRejected, openErr)
		}
		if created {
			mode := uint32(0755)
			if managed {
				mode = 0750
				if err = unix.Fchown(child, uid, gid); err != nil {
					_ = unix.Close(child)
					return fmt.Errorf("%w: own new mail storage directory: %w", ErrFailed, err)
				}
			}
			if err = unix.Fchmod(child, mode); err != nil {
				_ = unix.Close(child)
				return err
			}
		}
		var info unix.Stat_t
		if err = unix.Fstat(child, &info); err != nil {
			_ = unix.Close(child)
			return err
		}
		required := uint64(1)
		if i == len(parts)-1 {
			required = 3
		}
		// info.Mode is uint32 on Linux but uint16 on darwin; widen explicitly.
		mode := uint64(info.Mode)
		permissions := mode & 7
		if int(info.Uid) == uid {
			permissions = (mode >> 6) & 7
		} else if int(info.Gid) == gid {
			permissions = (mode >> 3) & 7
		}
		if uid != 0 && permissions&required != required || managed && mode&0002 != 0 {
			_ = unix.Close(child)
			return fmt.Errorf("%w: existing mail storage permissions do not safely permit mail delivery", ErrRejected)
		}
		if created {
			if err = syncMailStorageFD(child); err == nil {
				err = syncMailStorageFD(fd)
			}
			if err != nil {
				_ = unix.Close(child)
				return err
			}
		}
		_ = unix.Close(fd)
		fd = child
	}
	return nil
}

func syncMailStorageFD(fd int) error {
	err := unix.Fsync(fd)
	if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.ENOSYS) {
		return nil
	}
	return err
}
