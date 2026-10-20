package host

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Archive is a zip written entry by entry, every entry under one top-level
// directory. It is created exclusively and removed by its owner on a write
// error or an interrupt, as collect.Zip does: a truncated zip looks finished.
// dumps writes its archive through it too.
type Archive struct {
	path, name string
	f          *os.File
	zw         *zip.Writer
	err        error // the first error writing the file, kept
}

// CreateArchive creates <dir>/<name>.zip, refusing an existing name, a link
// included: O_EXCL, CREATE_NEW on Windows, does not follow it. On Windows the
// file gets a protected DACL (archiveSDDL) rather than inheriting the
// directory's, which gives BUILTIN\Users read access under C:\audit.
func CreateArchive(dir, name string) (*Archive, error) {
	return createArchive(dir, name, nil)
}

// createArchive is CreateArchive with wrap between the zip writer and the
// file, so a test can make the writes fail.
func createArchive(dir, name string, wrap func(io.Writer) io.Writer) (*Archive, error) {
	path := filepath.Join(dir, name+".zip")
	f, err := createExclusive(path)
	if err != nil {
		return nil, err
	}
	a := &Archive{path: path, name: name, f: f}
	var w io.Writer = f
	if wrap != nil {
		w = wrap(w)
	}
	a.zw = zip.NewWriter(stickyWriter{a, w})
	return a, nil
}

// archiveSDDL is the security descriptor of the archive on Windows: a
// protected DACL, nothing inherited, full access to SYSTEM, the
// Administrators and the user whose SID is given, and to no one else.
func archiveSDDL(sid string) string {
	return "D:P(A;;FA;;;SY)(A;;FA;;;BA)(A;;FA;;;" + sid + ")"
}

// stickyWriter keeps the first write error in the archive, which is how Put
// tells the file failing from the reader failing: io.Copy returns either.
type stickyWriter struct {
	a *Archive
	w io.Writer
}

func (s stickyWriter) Write(p []byte) (int, error) {
	if s.a.err != nil {
		return 0, s.a.err
	}
	n, err := s.w.Write(p)
	if err != nil {
		s.a.err = err
	}
	return n, err
}

// Path is the zip's path.
func (a *Archive) Path() string { return a.path }

// Put writes r as the entry name/<entry>. It is a FileSink. A write error is
// kept in Err and fails every later Put; a reader error leaves the entry cut
// short and the archive usable.
func (a *Archive) Put(entry string, r io.Reader) error {
	if a.err != nil {
		return a.err
	}
	w, err := a.zw.CreateHeader(&zip.FileHeader{Name: a.name + "/" + entry, Method: zip.Deflate, Modified: time.Now()})
	if err != nil {
		return err
	}
	_, err = io.Copy(w, r)
	if a.err != nil {
		return a.err
	}
	return err
}

// Err is the first error writing the zip, nil while it is whole.
func (a *Archive) Err() error { return a.err }

// Close writes the central directory and closes the file. Either can be where
// a full disk is reported, so either error is kept in Err.
func (a *Archive) Close() error {
	if err := a.zw.Close(); err != nil && a.err == nil {
		a.err = err
	}
	if err := a.f.Close(); err != nil && a.err == nil {
		a.err = err
	}
	return a.err
}

// Remove closes the file if it is open and deletes the zip, returning what
// the deletion returned.
func (a *Archive) Remove() error {
	a.f.Close()
	return os.Remove(a.path)
}
