// Package outputruntime implements the isolated notebook workflow stages.
package outputruntime

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// OpenRegular refuses symlinks in every path component and never blocks on FIFOs.
// Workflow scratch and participant mounts must be private to a stopped run.
func OpenRegular(name string, flags int) (*os.File, error) {
	absolute, err := filepath.Abs(name)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.TrimPrefix(absolute, "/"), "/")
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	for i, part := range parts {
		mode := unix.O_RDONLY | unix.O_DIRECTORY
		if i == len(parts)-1 {
			mode = flags | unix.O_NONBLOCK
		}
		next, e := unix.Openat(fd, part, mode|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
		unix.Close(fd)
		if e != nil {
			return nil, fmt.Errorf("open regular file: %w", e)
		}
		fd = next
	}
	f := os.NewFile(uintptr(fd), absolute)
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("expected regular file")
	}
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil || st.Nlink != 1 {
		f.Close()
		return nil, fmt.Errorf("hard links are not allowed")
	}
	return f, nil
}

func ReadBounded(name string, max int64) ([]byte, error) {
	f, err := OpenRegular(name, unix.O_RDONLY)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("file exceeds size limit")
	}
	return data, nil
}

func WriteFile(name string, data []byte) error {
	// Do not truncate until the descriptor has passed regular-file/link checks.
	f, err := OpenRegular(name, unix.O_WRONLY|unix.O_CREAT)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := f.Truncate(0); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	return f.Sync()
}
