package serviceregistry

import (
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// The approved private source root is the custody boundary. Shared Box/agent
// ancestors need traversal, not a fabricated requirement that every directory
// from / be owned by the credential user. Hold no-follow descriptors throughout.
func applicationRuntimeCredentialSourceParent(path string, uid uint32, roots []string) (*os.File, error) {
	root := ""
	for _, candidate := range roots {
		if applicationAbsolutePath(candidate) && strings.HasPrefix(path, candidate+"/") && len(candidate) > len(root) {
			root = candidate
		}
	}
	if root == "" || !applicationAbsolutePath(path) {
		return nil, applicationError("credential.import_source_denied")
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	current := ""
	for _, part := range strings.Split(strings.TrimPrefix(filepath.Dir(path), "/"), "/") {
		current += "/" + part
		next, e := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if e != nil {
			return nil, e
		}
		fd = next
		var st unix.Stat_t
		if unix.Fstat(fd, &st) != nil {
			unix.Close(fd)
			return nil, applicationError("credential.import_source_custody")
		}
		if current == root || strings.HasPrefix(current, root+"/") {
			if st.Uid != uid || st.Mode&07777 != 0700 {
				unix.Close(fd)
				return nil, applicationError("credential.import_source_custody")
			}
		}
	}
	return os.NewFile(uintptr(fd), filepath.Dir(path)), nil
}
