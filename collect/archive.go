package collect

import (
	"archive/zip"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// archivable admits only regular files. WalkDir lstats, so a symlink arrives
// at the walk function as a symlink: the header would keep the link bit while
// os.Open follows the link, pulling the target's whole content into an archive
// that tells its reader it holds nothing but server metadata. Devices, sockets
// and pipes have no business in a collection either, and opening one can block
// the run indefinitely.
func archivable(fi fs.FileInfo) bool { return fi.Mode().IsRegular() }

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
	// OpenFile rather than Create: Create is 0666 before umask and takes no
	// argument to say otherwise, and this file is the whole run in the form
	// that gets mailed onward.
	out, err := os.OpenFile(destZip, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, filePerm)
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
		if !archivable(fi) {
			return nil
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
		return copyInto(w, p)
	})
	if walkErr != nil {
		zw.Close()
		return walkErr
	}
	return zw.Close()
}

// copyInto exists so that each archived file is closed when its own copy ends
// rather than when the whole walk does.
//
// A `defer f.Close()` inside the WalkDir callback defers to the callback's
// return, which reads correctly and is correct — but the callback is one
// closure invoked once per file, so a run over a few thousand files holds a few
// thousand descriptors at once. A collection with --include-object-definitions
// against databases carrying a few thousand modules writes one file per module
// and passes the 1024-descriptor limit of an ordinary Unix account. Zip then
// fails, and the error path of the caller deletes the half-written archive:
// a run that did all its work on the instance ends with nothing to show for it.
func copyInto(w io.Writer, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}
