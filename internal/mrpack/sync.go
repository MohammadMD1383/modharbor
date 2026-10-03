package mrpack

import "os"

// syncDir fsyncs the directory so a rename into it is durable: without this
// a crash right after os.Rename can leave the entry missing even though the
// file contents were synced. Errors opening or syncing the directory are
// returned; a missing directory is not an error the caller can usefully
// distinguish here, so it propagates like any other failure.
func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
