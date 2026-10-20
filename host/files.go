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

// PathCheck says whether a file may be read. On Windows: its final path is on
// a local fixed drive.
type PathCheck func(path string) (ok bool, reason string)

const (
	jobOutputFiles = 5       // per step, the newest
	jobOutputTail  = 1 << 20 // bytes kept of each
)

// CopyExtensions lists every file under root and copies those modified on or
// after since, newest first, until the next one would exceed budget. The
// paths given to check and listed are those of fsys. A missing root is
// returned as an error wrapping fs.ErrNotExist.
func CopyExtensions(fsys fs.FS, root string, since time.Time, budget int64, check PathCheck, sink FileSink) ([]Listed, error) {
	var listed []Listed
	var mods []time.Time
	err := fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		listed = append(listed, Listed{Path: p, Size: info.Size(), Modified: info.ModTime().UTC().Format(time.RFC3339)})
		mods = append(mods, info.ModTime())
		return nil
	})
	if err != nil {
		return nil, err
	}

	order := make([]int, len(listed))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(a, b int) bool { return mods[order[a]].After(mods[order[b]]) })
	var used int64
	full := false
	for _, i := range order {
		l := &listed[i]
		switch {
		case mods[i].Before(since):
			l.Reason = "before the window"
			continue
		case full || used+l.Size > budget:
			full = true
			l.Reason = fmt.Sprintf("over the %d MiB budget", budget>>20)
			continue
		}
		if ok, why := check(l.Path); !ok {
			l.Reason = why
			continue
		}
		rel := l.Path
		if root != "." {
			rel = strings.TrimPrefix(l.Path, root+"/")
		}
		if err := copyFile(fsys, l.Path, l.Size, "060.vm-extensions/"+rel, sink); err != nil {
			return listed, err
		}
		l.Copied = true
		used += l.Size
	}
	return listed, nil
}

// copyFile writes the first size bytes of a file, the size it was listed at:
// a log written to during the copy does not overrun the budget.
func copyFile(fsys fs.FS, name string, size int64, archived string, sink FileSink) error {
	f, err := fsys.Open(name)
	if err != nil {
		return err
	}
	defer f.Close()
	return sink(archived, io.LimitReader(f, size))
}

// CopyJobOutputs copies the five newest files of p.Dir whose name p.Name
// matches and modified on or after since, each cut to its last MiB, under
// 070.job-outputs/<step>/. What is not read and why goes in the notes: an
// unreadable pattern, a UNC directory, a wildcard token, no matching file, a
// file check refuses.
func CopyJobOutputs(open func(dir string) (fs.FS, error), p OutputPattern, step string, since time.Time, check PathCheck, sink FileSink) ([]CopiedFile, []string, error) {
	if !p.Readable {
		return nil, []string{p.Reason}, nil
	}
	if IsUNC(p.Dir) {
		return nil, []string{"UNC path, not read: " + p.Dir}, nil
	}
	var notes []string
	if p.Reason != "" {
		notes = append(notes, p.Reason)
	}
	fsys, err := open(p.Dir)
	if err != nil {
		return nil, notes, err
	}
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, notes, err
	}
	var infos []fs.FileInfo
	for _, e := range entries {
		if !e.Type().IsRegular() || !p.Name.MatchString(e.Name()) {
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
		src := strings.TrimRight(p.Dir, `\/`) + `\` + info.Name()
		if ok, why := check(src); !ok {
			notes = append(notes, src+": "+why)
			continue
		}
		c := CopiedFile{
			Source:   src,
			Archived: path.Join("070.job-outputs", step, info.Name()),
			Size:     info.Size(),
			Modified: info.ModTime().UTC().Format(time.RFC3339),
		}
		if c.Cut, err = copyTail(fsys, info.Name(), info.Size(), c.Archived, sink); err != nil {
			return copied, notes, err
		}
		copied = append(copied, c)
	}
	return copied, notes, nil
}

func copyTail(fsys fs.FS, name string, size int64, archived string, sink FileSink) (bool, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return false, err
	}
	defer f.Close()
	rs, ok := f.(io.ReadSeeker)
	if !ok {
		return false, fmt.Errorf("%s: file cannot seek", name)
	}
	r, cut, err := TailReader(rs, size, jobOutputTail)
	if err != nil {
		return false, err
	}
	return cut, sink(archived, r)
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
