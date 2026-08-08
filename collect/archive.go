package collect

import (
	"archive/zip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// Zip packages runFolder into destZip, keeping the run folder as the single
// top-level directory inside the archive so it cannot explode into the
// recipient's working directory.
//
// destZip may legitimately sit inside runFolder — writing it next to the
// collected files is the obvious thing for a caller to do — so the archive
// skips itself rather than trying to add a file that is still being written.
//
// A failed archive is removed. Leaving a truncated .zip behind is worse than
// leaving none: it looks finished, and the failure is only discovered by
// whoever receives it.
func Zip(runFolder, destZip string) (err error) {
	if fi, statErr := os.Stat(runFolder); statErr != nil {
		return statErr
	} else if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", runFolder)
	}
	out, err := os.Create(destZip)
	if err != nil {
		return err
	}
	defer func() {
		// Close errors matter here: the zip writer flushes the central
		// directory on Close, and the file's own Close is where a full disk is
		// finally reported. Either one silently dropped yields an archive that
		// no reader can open.
		if cerr := out.Close(); cerr != nil && err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(destZip)
		}
	}()

	// Compared by path identity so the archive skips itself even when the
	// caller passed a relative destination.
	self, err := filepath.Abs(destZip)
	if err != nil {
		return err
	}

	zw := zip.NewWriter(out)
	base := filepath.Base(runFolder)
	walkErr := filepath.WalkDir(runFolder, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if abs, aerr := filepath.Abs(p); aerr == nil && abs == self {
			return nil
		}
		rel, err := filepath.Rel(runFolder, p)
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		hdr, err := zip.FileInfoHeader(fi)
		if err != nil {
			return err
		}
		// Zip entries always use forward slashes, whatever the host separator.
		hdr.Name = base + "/" + filepath.ToSlash(rel)
		// JSON compresses very well and these archives are mailed around.
		hdr.Method = zip.Deflate
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(w, f)
		return err
	})
	if walkErr != nil {
		zw.Close()
		return walkErr
	}
	return zw.Close()
}
