package host

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"
)

// CopiedFile is one file copied into the archive.
type CopiedFile struct {
	Source   string `json:"source"`
	Archived string `json:"archived"`
	Size     int64  `json:"size"` // of the source file
	Cut      bool   `json:"cut"`  // only its tail was copied
	Modified string `json:"modified_utc"`
}

// Listed is one file of the VM extension logs, copied or not.
type Listed struct {
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Modified string `json:"modified_utc"`
	Copied   bool   `json:"copied"`
	Reason   string `json:"reason,omitempty"`
}

// FileSink writes one archive entry.
type FileSink func(archived string, r io.Reader) error

// PathCheck says whether a directory may be listed, judged before anything
// opens it. On Windows: on a local fixed drive, no symbolic link on the way.
type PathCheck func(path string) (ok bool, reason string)

// OpenFile opens a file to copy it, once: the handle it returns is the one
// judged, so nothing swapped in between a check and an open is read. A reason
// means refused, and f is nil. On Windows: the path itself is not followed if
// it is a link, a link or a directory is refused, and the handle's final path
// must lie on a local fixed drive. size is read from the same handle.
type OpenFile func(path string) (f io.ReadSeekCloser, size int64, reason string, err error)

const (
	jobOutputFiles = 5       // per step, the newest
	jobOutputTail  = 1 << 20 // bytes kept of each
)

// CopyExtensions lists every file under fsys and copies those modified on or
// after since, newest first, until the next one would exceed budget. fsys is
// rooted at root, the Windows directory it reads: the paths given to open and
// listed are root joined to the relative path, with backslashes. A directory
// that cannot be read and a link, which is not followed, are listed with the
// reason. A missing root is returned as an error wrapping fs.ErrNotExist.
func CopyExtensions(fsys fs.FS, root string, since time.Time, budget int64, open OpenFile, sink FileSink) ([]Listed, error) {
	var listed []Listed
	var files []int // the regular files: their index in listed, path in fsys, time
	var rels []string
	var mods []time.Time
	winPath := func(rel string) string {
		if rel == "." {
			return root
		}
		return root + `\` + strings.ReplaceAll(rel, "/", `\`)
	}
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil && d == nil: // the root itself
			return err
		case err != nil:
			listed = append(listed, Listed{Path: winPath(p), Reason: "unreadable: " + err.Error()})
			return nil
		case d.Type()&(fs.ModeSymlink|fs.ModeIrregular) != 0:
			// Since Go 1.23 a junction is ModeIrregular, not ModeSymlink.
			listed = append(listed, Listed{Path: winPath(p), Reason: "link, not followed"})
			return nil
		case !d.Type().IsRegular():
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		files = append(files, len(listed))
		listed = append(listed, Listed{Path: winPath(p), Size: info.Size(), Modified: info.ModTime().UTC().Format(time.RFC3339)})
		rels = append(rels, p)
		mods = append(mods, info.ModTime())
		return nil
	})
	if err != nil {
		return nil, err
	}

	order := make([]int, len(files))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return mods[order[a]].After(mods[order[b]]) })
	var used int64
	full := false
	for _, k := range order {
		l := &listed[files[k]]
		switch {
		case mods[k].Before(since):
			l.Reason = "before the window"
			continue
		case full || used+l.Size > budget:
			full = true
			l.Reason = fmt.Sprintf("over the %d MiB budget", budget>>20)
			continue
		}
		f, _, why, err := open(l.Path)
		if err != nil {
			return listed, err
		}
		if why != "" {
			l.Reason = why
			continue
		}
		// The listed size, which the budget counted: a log written to during
		// the copy does not overrun it.
		err = sink("060.vm-extensions/"+rels[k], io.LimitReader(f, l.Size))
		f.Close()
		if err != nil {
			return listed, err
		}
		l.Copied = true
		used += l.Size
	}
	return listed, nil
}

// CopyJobOutputs copies the five newest files of p.Dir whose name p.Name
// matches and modified on or after since, each cut to its last MiB, under
// 070.job-outputs/<step>/. The directory is judged by c.DirCheck before it is
// listed, and each file by the handle c.OpenFile copies it from. A link is
// not followed: a matching one is noted, so a planted link cannot export a
// private file under the log's name. What is not read and why goes in the
// notes: an unreadable pattern, a UNC or relative directory, a directory or a
// file refused, a link, a wildcard token, no matching file.
func CopyJobOutputs(c Calls, p OutputPattern, step string, since time.Time, sink FileSink) ([]CopiedFile, []string, error) {
	if !p.Readable {
		return nil, []string{p.Reason}, nil
	}
	if IsUNC(p.Dir) {
		return nil, []string{"UNC path, not read: " + p.Dir}, nil
	}
	if !isDriveAbsolute(p.Dir) {
		return nil, []string{p.Dir + ": not an absolute path"}, nil
	}
	var notes []string
	if p.Reason != "" {
		notes = append(notes, p.Reason)
	}
	if ok, why := c.DirCheck(p.Dir); !ok {
		return nil, append(notes, p.Dir+": "+why), nil
	}
	fsys, err := c.OpenDir(p.Dir)
	if err != nil {
		return nil, notes, err
	}
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, notes, err
	}
	var infos []fs.FileInfo
	for _, e := range entries {
		switch {
		case !p.Name.MatchString(e.Name()):
			continue
		case e.Type()&(fs.ModeSymlink|fs.ModeIrregular) != 0:
			// Since Go 1.23 a junction is ModeIrregular, not ModeSymlink.
			notes = append(notes, e.Name()+": link, not followed")
			continue
		case !e.Type().IsRegular():
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, notes, err
		}
		if !info.ModTime().Before(since) {
			infos = append(infos, info)
		}
	}
	if len(infos) == 0 {
		return nil, append(notes, p.Dir+": matches nothing in the window"), nil
	}
	sort.SliceStable(infos, func(a, b int) bool { return infos[a].ModTime().After(infos[b].ModTime()) })
	if len(infos) > jobOutputFiles {
		infos = infos[:jobOutputFiles]
	}

	var copied []CopiedFile
	for _, info := range infos {
		// The path opened is the file's final path: no link is followed, in
		// the directory (DirCheck) or at the file (OpenFile).
		src := strings.TrimRight(p.Dir, `\/`) + `\` + info.Name()
		f, size, why, err := c.OpenFile(src)
		if err != nil {
			return copied, notes, err
		}
		if why != "" {
			notes = append(notes, src+": "+why)
			continue
		}
		cf := CopiedFile{
			Source:   src,
			Archived: path.Join("070.job-outputs", step, info.Name()),
			Size:     size,
			Modified: info.ModTime().UTC().Format(time.RFC3339),
		}
		var r io.Reader
		r, cf.Cut, err = TailReader(f, size, jobOutputTail)
		if err == nil {
			err = sink(cf.Archived, r)
		}
		f.Close()
		if err != nil {
			return copied, notes, err
		}
		copied = append(copied, cf)
	}
	return copied, notes, nil
}

// TailReader returns the last limit bytes of a file of the given size,
// reached by Seek so a large file is never read through, and whether it was
// cut.
func TailReader(f io.ReadSeeker, size, limit int64) (io.Reader, bool, error) {
	if size <= limit {
		return io.LimitReader(f, size), false, nil
	}
	if _, err := f.Seek(size-limit, io.SeekStart); err != nil {
		return nil, false, err
	}
	return io.LimitReader(f, limit), true, nil
}

// isDriveAbsolute reports X:\ or X:/, the only directory form read: a relative
// or drive-relative (C:Logs) path depends on the current directory.
func isDriveAbsolute(p string) bool {
	return len(p) >= 3 && ('A' <= p[0] && p[0] <= 'Z' || 'a' <= p[0] && p[0] <= 'z') &&
		p[1] == ':' && (p[2] == '\\' || p[2] == '/')
}

// IsUNC reports a network path: \\server\share, \\?\UNC\ or \\.\UNC\, and
// //server/share. \\?\C:\ and \\.\C:\ are local.
func IsUNC(p string) bool {
	sep := func(i int) bool { return len(p) > i && (p[i] == '\\' || p[i] == '/') }
	if !sep(0) || !sep(1) || len(p) < 3 {
		return false
	}
	if p[2] == '?' || p[2] == '.' {
		return sep(3) && sep(7) && strings.EqualFold(p[4:7], "UNC")
	}
	return true
}
