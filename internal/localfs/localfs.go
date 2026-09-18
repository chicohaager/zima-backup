// Package localfs holds the one rule both runners share for local targets.
package localfs

import (
	"fmt"
	"os"
	"path/filepath"
)

// EnsureTarget creates path only when its parent already exists: an
// unmounted disk must not turn into a copy inside the empty mount point.
func EnsureTarget(path string) error {
	parent := filepath.Dir(filepath.Clean(path))
	st, err := os.Stat(parent)
	if err != nil || !st.IsDir() {
		return fmt.Errorf("target folder %s is not available (disk not mounted?)", parent)
	}
	return os.MkdirAll(path, 0700)
}
