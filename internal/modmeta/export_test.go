package modmeta

import (
	"archive/zip"
	"io"
)

// newZipWriter is a thin indirection so tests do not need to import
// archive/zip themselves.
func newZipWriter(w io.Writer) *zip.Writer { return zip.NewWriter(w) }
