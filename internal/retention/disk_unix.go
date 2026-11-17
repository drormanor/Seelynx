//go:build !windows

package retention

import "golang.org/x/sys/unix"

// DiskUsage returns free and total bytes on the volume holding path.
func DiskUsage(path string) (free, total uint64, err error) {
	var st unix.Statfs_t
	if err = unix.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	bs := uint64(st.Bsize)
	return uint64(st.Bavail) * bs, uint64(st.Blocks) * bs, nil
}
