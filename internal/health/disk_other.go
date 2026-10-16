//go:build !unix

package health

import "errors"

// diskUsage is not available on this system; the disk check reports an error.
func diskUsage(string) (free, total uint64, err error) {
	return 0, 0, errors.New("disk space cannot be read on this system")
}
