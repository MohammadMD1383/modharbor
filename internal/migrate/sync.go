package migrate

import "os"

// syncDir fsyncs the directory so a rename into it is durable: without this
// a crash right after os.Rename can leave the entry missing even though the
// file contents were synced. It mirrors internal/mrpack/sync.go, which covers
// the mrpack install path; this one covers the migrate jar-install path.
func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
