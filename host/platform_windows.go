package host

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

// kernel32 is the one DLL loaded by name: Go loads it itself, from the system
// directory. Every other DLL goes through systemDLL, since the process is
// elevated and a bare name would let a DLL left beside the executable be
// loaded instead (see syscall.LoadDLL).
var (
	kernel32                         = syscall.NewLazyDLL("kernel32.dll")
	procGetSystemDirectoryW          = kernel32.NewProc("GetSystemDirectoryW")
	procGetComputerNameExW           = kernel32.NewProc("GetComputerNameExW")
	procGetFinalPathNameByHandleW    = kernel32.NewProc("GetFinalPathNameByHandleW")
	procGetVolumePathNameW           = kernel32.NewProc("GetVolumePathNameW")
	procGetDriveTypeW                = kernel32.NewProc("GetDriveTypeW")
	procLocalFree                    = kernel32.NewProc("LocalFree")
	procSetDefaultDllDirectories     = kernel32.NewProc("SetDefaultDllDirectories")
	procGetFileInformationByHandleEx = kernel32.NewProc("GetFileInformationByHandleEx")
	procReOpenFile                   = kernel32.NewProc("ReOpenFile")

	advapi32                  = systemDLL("advapi32.dll")
	procOpenSCManagerW        = advapi32.NewProc("OpenSCManagerW")
	procEnumServicesStatusExW = advapi32.NewProc("EnumServicesStatusExW")
	procCloseServiceHandle    = advapi32.NewProc("CloseServiceHandle")

	procConvertStringSecurityDescriptorToSecurityDescriptorW = advapi32.NewProc("ConvertStringSecurityDescriptorToSecurityDescriptorW")

	fltlib              = systemDLL("fltlib.dll")
	procFilterFindFirst = fltlib.NewProc("FilterFindFirst")
	procFilterFindNext  = fltlib.NewProc("FilterFindNext")
	procFilterFindClose = fltlib.NewProc("FilterFindClose")

	powrprof                  = systemDLL("powrprof.dll")
	procPowerGetActiveScheme  = powrprof.NewProc("PowerGetActiveScheme")
	procPowerReadFriendlyName = powrprof.NewProc("PowerReadFriendlyName")
)

// systemDir is GetSystemDirectoryW's answer. It cannot fail on a running
// Windows; if it did, no DLL is loaded by a bare name in its place.
var systemDir = sync.OnceValue(func() string {
	buf := make([]uint16, syscall.MAX_PATH)
	for {
		n, _, err := procGetSystemDirectoryW.Call(uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		switch {
		case n == 0:
			panic(fmt.Sprintf("GetSystemDirectoryW: %v", err))
		case int(n) > len(buf):
			buf = make([]uint16, n)
			continue
		}
		return syscall.UTF16ToString(buf[:n])
	}
})

// systemDLL loads name by its absolute path in the system directory. The
// absolute path covers the DLL itself, not its own imports, which the loader
// searches from the executable's directory first: restrictDLLSearch covers
// those.
func systemDLL(name string) *syscall.LazyDLL {
	return syscall.NewLazyDLL(filepath.Join(systemDir(), name))
}

// dllSearchErr is restrictDLLSearch's answer, which Check returns: the host
// does not run with its DLLs' imports searched beside the executable.
var dllSearchErr error

// restrictDLLSearch makes System32 the only directory the loader searches,
// for the DLLs this process loads and for their imports
// (SetDefaultDllDirectories, LOAD_LIBRARY_SEARCH_SYSTEM32). An elevated
// sql-auditor.exe run from Downloads would otherwise load an import of
// powrprof.dll or wevtapi.dll that a browser left there. It is called by
// WindowsPlatform, which main evaluates before Run makes the host's first
// call into a DLL, and not in an init: the host package is imported by every
// command of the binary, and only this one is meant to run elevated.
func restrictDLLSearch() error {
	const loadLibrarySearchSystem32 = 0x800
	if r, _, err := procSetDefaultDllDirectories.Call(loadLibrarySearchSystem32); r == 0 {
		return fmt.Errorf("SetDefaultDllDirectories: %w", err)
	}
	return nil
}

// WindowsPlatform reads this machine, and the instance through inst.
func WindowsPlatform(inst *Instance) Platform {
	dllSearchErr = restrictDLLSearch()
	return Platform{
		Check: func() error {
			if dllSearchErr != nil {
				return dllSearchErr
			}
			return checkElevated()
		},
		NetBIOS:  netBIOS,
		Services: services,
		Facts:    inst.Facts,
		Steps:    inst.Steps,
		Sources:  BuildSources(windowsCalls()),
	}
}

func windowsCalls() Calls {
	pf := os.Getenv("ProgramFiles")
	if pf == "" {
		pf = `C:\Program Files`
	}
	return Calls{
		Query:        evtQuery,
		Coverage:     evtCoverage,
		PowerShell:   powerShell,
		Filters:      filters,
		Power:        power,
		OpenDir:      func(dir string) (fs.FS, error) { return os.DirFS(dir), nil },
		ReadFile:     os.ReadFile,
		DirCheck:     dirCheck,
		OpenFile:     openFile,
		ProgramFiles: pf,
		AzurePlugins: `C:\WindowsAzure\Logs\Plugins`,
	}
}

var errNotElevated = errors.New("sql-auditor host reads the event logs and the service manager: run from an elevated prompt")

// checkElevated reads TokenElevation from the process token.
func checkElevated() error {
	p, err := syscall.GetCurrentProcess()
	if err != nil {
		return err
	}
	var t syscall.Token
	if err := syscall.OpenProcessToken(p, syscall.TOKEN_QUERY, &t); err != nil {
		return fmt.Errorf("%w (%v)", errNotElevated, err)
	}
	defer t.Close()
	var elevated, n uint32
	if err := syscall.GetTokenInformation(t, syscall.TokenElevation, (*byte)(unsafe.Pointer(&elevated)), 4, &n); err != nil {
		return fmt.Errorf("%w (%v)", errNotElevated, err)
	}
	if elevated == 0 {
		return errNotElevated
	}
	return nil
}

// netBIOS is ComputerNamePhysicalNetBIOS (4), what the instance's
// SERVERPROPERTY of the same name is compared with.
func netBIOS() (string, error) {
	n := uint32(16)
	for range 2 {
		buf := make([]uint16, n)
		r, _, err := procGetComputerNameExW.Call(4, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)))
		if r != 0 {
			return syscall.UTF16ToString(buf[:n]), nil
		}
		if err != syscall.ERROR_MORE_DATA {
			return "", fmt.Errorf("GetComputerNameExW: %w", err)
		}
	}
	return "", errors.New("GetComputerNameExW: buffer still too small")
}

// enumServiceStatusProcess is ENUM_SERVICE_STATUS_PROCESSW.
type enumServiceStatusProcess struct {
	ServiceName, DisplayName *uint16
	Status                   [9]uint32 // SERVICE_STATUS_PROCESS
}

// services returns the name of every Win32 service, whatever its state.
func services() ([]string, error) {
	const scManagerEnumerateService, scEnumProcessInfo, serviceWin32, serviceStateAll = 0x0004, 0, 0x30, 3
	h, _, err := procOpenSCManagerW.Call(0, 0, scManagerEnumerateService)
	if h == 0 {
		return nil, fmt.Errorf("OpenSCManagerW: %w", err)
	}
	defer procCloseServiceHandle.Call(h)
	var names []string
	var resume uint32
	buf := make([]uint64, 8<<10) // 64 KiB, pointer-aligned
	for {
		var needed, returned uint32
		r, _, err := procEnumServicesStatusExW.Call(h, scEnumProcessInfo, serviceWin32, serviceStateAll,
			uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)*8),
			uintptr(unsafe.Pointer(&needed)), uintptr(unsafe.Pointer(&returned)), uintptr(unsafe.Pointer(&resume)), 0)
		if r == 0 && err != syscall.ERROR_MORE_DATA {
			return nil, fmt.Errorf("EnumServicesStatusExW: %w", err)
		}
		for _, s := range unsafe.Slice((*enumServiceStatusProcess)(unsafe.Pointer(&buf[0])), returned) {
			names = append(names, utf16PtrToString(s.ServiceName))
		}
		if r != 0 {
			return names, nil
		}
		if int(needed) > len(buf)*8 {
			buf = make([]uint64, (needed+7)/8)
		}
	}
}

func utf16PtrToString(p *uint16) string {
	if p == nil {
		return ""
	}
	n := 0
	for *(*uint16)(unsafe.Add(unsafe.Pointer(p), 2*n)) != 0 {
		n++
	}
	return string(utf16.Decode(unsafe.Slice(p, n)))
}

// powerShell runs a script as psCommand builds it, with PSModulePath narrowed
// to the system modules (psEnv; the script's psPreamble is the real guard).
func powerShell(ctx context.Context, script string) ([]byte, error) {
	cmd, _, cancel := psCommand(ctx, systemDir(), script, os.Environ())
	defer cancel()
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("powershell: %v: %s", err, strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("powershell: %w", err)
	}
	return out, nil
}

// filters lists the file system filter drivers through FilterFindFirst and
// FilterFindNext, class FilterAggregateStandardInformation (2).
func filters() ([]Filter, error) {
	const aggregateStandard = 2
	const hrNoMoreItems = 0x80070103        // HRESULT_FROM_WIN32(ERROR_NO_MORE_ITEMS)
	const hrInsufficientBuffer = 0x8007007A // HRESULT_FROM_WIN32(ERROR_INSUFFICIENT_BUFFER)
	const hrMoreData = 0x800700EA           // HRESULT_FROM_WIN32(ERROR_MORE_DATA)
	buf := make([]byte, 1024)
	var h uintptr
	var out []Filter
	for {
		var got uint32
		var hr uintptr
		if h == 0 {
			hr, _, _ = procFilterFindFirst.Call(aggregateStandard, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&got)), uintptr(unsafe.Pointer(&h)))
		} else {
			hr, _, _ = procFilterFindNext.Call(h, aggregateStandard, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&got)))
		}
		switch uint32(hr) {
		case 0:
			out = append(out, parseFilter(buf[:got]))
			continue
		case hrNoMoreItems:
			if h != 0 {
				procFilterFindClose.Call(h)
			}
			return out, nil
		case hrInsufficientBuffer, hrMoreData:
			if int(got) > len(buf) {
				buf = make([]byte, got)
				continue
			}
		}
		if h != 0 {
			procFilterFindClose.Call(h)
		}
		return nil, fmt.Errorf("FilterFind: HRESULT 0x%08X", uint32(hr))
	}
}

// parseFilter reads one FILTER_AGGREGATE_STANDARD_INFORMATION, whose name and
// altitude offsets count from the start of the structure.
func parseFilter(b []byte) Filter {
	u16 := func(i int) int {
		if i+2 > len(b) {
			return 0
		}
		return int(binary.LittleEndian.Uint16(b[i:]))
	}
	u32 := func(i int) int {
		if i+4 > len(b) {
			return 0
		}
		return int(binary.LittleEndian.Uint32(b[i:]))
	}
	str := func(lenAt, offAt int) string {
		n, off := u16(lenAt), u16(offAt)
		if off+n > len(b) {
			return ""
		}
		u := make([]uint16, n/2)
		for i := range u {
			u[i] = binary.LittleEndian.Uint16(b[off+2*i:])
		}
		return string(utf16.Decode(u))
	}
	const legacyFlag = 2 // FLTFL_ASI_IS_LEGACYFILTER
	if u32(4)&legacyFlag != 0 {
		return Filter{Name: str(12, 14), Altitude: str(16, 18), Legacy: true}
	}
	return Filter{Name: str(20, 22), Altitude: str(24, 26), Frame: u32(12), Instances: u32(16)}
}

type guid struct {
	Data1        uint32
	Data2, Data3 uint16
	Data4        [8]byte
}

func (g *guid) String() string {
	return fmt.Sprintf("%08x-%04x-%04x-%x-%x", g.Data1, g.Data2, g.Data3, g.Data4[:2], g.Data4[2:])
}

// power reads the active scheme's GUID and friendly name.
func power() (PowerPlan, error) {
	var g *guid
	if r, _, _ := procPowerGetActiveScheme.Call(0, uintptr(unsafe.Pointer(&g))); r != 0 {
		return PowerPlan{}, fmt.Errorf("PowerGetActiveScheme: %w", syscall.Errno(r))
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(g)))
	p := PowerPlan{GUID: g.String()}
	var size uint32
	if r, _, _ := procPowerReadFriendlyName.Call(0, uintptr(unsafe.Pointer(g)), 0, 0, 0, uintptr(unsafe.Pointer(&size))); r != 0 || size < 2 {
		return p, nil // the GUID is what tells the plans apart; the name is a courtesy
	}
	buf := make([]uint16, (size+1)/2)
	if r, _, _ := procPowerReadFriendlyName.Call(0, uintptr(unsafe.Pointer(g)), 0, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size))); r == 0 {
		p.Name = syscall.UTF16ToString(buf)
	}
	return p, nil
}

const fileShareAll = syscall.FILE_SHARE_READ | syscall.FILE_SHARE_WRITE | syscall.FILE_SHARE_DELETE

// openFile opens path for reading without following it if it is a link, and
// judges the handle it returns: a name surrogate (a symbolic link, a
// junction), a file with more than one name (a hard link planted to a private
// file reads as the log) or a directory is refused, and so is a final path off
// a local fixed volume. Another reparse point, a deduplicated file, is
// reopened through the same handle without FILE_FLAG_OPEN_REPARSE_POINT, so
// that the filter which owns it gives the file's data rather than its stub (a
// WOF-compressed file never gets here: Go lists it as irregular, and the
// listing records it as a link). Opening fails as a refusal, as a file
// rotated away between the listing and the copy.
func openFile(path string) (io.ReadSeekCloser, int64, string, string, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, 0, "", err.Error(), nil
	}
	h, err := syscall.CreateFile(p, syscall.GENERIC_READ, fileShareAll, nil,
		syscall.OPEN_EXISTING, syscall.FILE_FLAG_OPEN_REPARSE_POINT|syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return nil, 0, "", "not opened: " + err.Error(), nil
	}
	var fi syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(h, &fi); err != nil {
		syscall.CloseHandle(h)
		return nil, 0, "", "", fmt.Errorf("%s: %w", path, err)
	}
	why := ""
	if fi.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		why = reparseFile(&h)
	}
	switch {
	case why != "":
	case fi.FileAttributes&syscall.FILE_ATTRIBUTE_DIRECTORY != 0:
		why = "a directory"
	case fi.NumberOfLinks > 1:
		why = "hard link, not read"
	}
	final := ""
	if why == "" {
		final, why = localFixed(h)
	}
	if why != "" {
		syscall.CloseHandle(h)
		return nil, 0, "", why, nil
	}
	return os.NewFile(uintptr(h), path), int64(fi.FileSizeHigh)<<32 | int64(fi.FileSizeLow), final, "", nil
}

// reparseFile judges a file that is a reparse point by its tag
// (FileAttributeTagInfo): a name surrogate is refused, and anything else is
// reopened from *h without FILE_FLAG_OPEN_REPARSE_POINT (ReOpenFile, which
// opens the same file and resolves no path), *h then replaced.
func reparseFile(h *syscall.Handle) string {
	const fileAttributeTagInfo, nameSurrogate = 9, 0x20000000
	var tag struct{ FileAttributes, ReparseTag uint32 } // FILE_ATTRIBUTE_TAG_INFO
	if r, _, err := procGetFileInformationByHandleEx.Call(uintptr(*h), fileAttributeTagInfo, uintptr(unsafe.Pointer(&tag)), unsafe.Sizeof(tag)); r == 0 {
		return "reparse tag: " + err.Error()
	}
	if tag.ReparseTag&nameSurrogate != 0 {
		return "link, not followed"
	}
	r, _, err := procReOpenFile.Call(uintptr(*h), syscall.GENERIC_READ, fileShareAll, 0)
	if syscall.Handle(r) == syscall.InvalidHandle {
		return fmt.Sprintf("reparse point (tag 0x%08X) not reopened: %v", tag.ReparseTag, err)
	}
	syscall.CloseHandle(*h)
	*h = syscall.Handle(r)
	return ""
}

// finalPath is GetFinalPathNameByHandleW's answer for h, \\?\ included.
func finalPath(h syscall.Handle) (string, error) {
	buf := make([]uint16, syscall.MAX_PATH)
	for {
		n, _, err := procGetFinalPathNameByHandleW.Call(uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0)
		if n == 0 {
			return "", fmt.Errorf("final path: %w", err)
		}
		if int(n) < len(buf) {
			return syscall.UTF16ToString(buf[:n]), nil
		}
		buf = make([]uint16, n)
	}
}

// localFixed returns the final path of an open file and accepts it when it
// lies on a local fixed volume: the reason is "" then. The volume is the one
// GetVolumePathNameW returns, so a mount point on a local disk is judged as
// its own volume.
func localFixed(h syscall.Handle) (string, string) {
	final, err := finalPath(h)
	if err != nil {
		return "", err.Error()
	}
	if strings.HasPrefix(final, `\\?\UNC\`) {
		return "", "network path " + final
	}
	if _, why := fixedVolume(strings.TrimPrefix(final, `\\?\`)); why != "" {
		return "", why
	}
	return final, ""
}

const driveFixed = 3 // DRIVE_FIXED; a mapped drive is DRIVE_REMOTE (4)

// fixedVolume accepts a path whose volume, by GetVolumePathNameW, is a local
// fixed drive.
func fixedVolume(path string) (bool, string) {
	fp, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return false, err.Error()
	}
	root := make([]uint16, len(path)+2)
	if r, _, err := procGetVolumePathNameW.Call(uintptr(unsafe.Pointer(fp)), uintptr(unsafe.Pointer(&root[0])), uintptr(len(root))); r == 0 {
		return false, "volume of " + path + ": " + err.Error()
	}
	if t, _, _ := procGetDriveTypeW.Call(uintptr(unsafe.Pointer(&root[0]))); t != driveFixed {
		return false, fmt.Sprintf("%s is not on a local fixed drive (drive type %d)", path, t)
	}
	return true, ""
}

// dirCheck judges a directory as typed without opening it, so that a network
// directory is refused before anything is sent to it, and gives its final
// path. The drive letter must be a local fixed drive (a mapped drive is not);
// then each component is read from its parent's listing by FindFirstFileW,
// which does not traverse it, and refused if it is a reparse point other than
// a volume mount point, which can only lead to a local volume: a symbolic link
// can lead to \\server\share, and a directory junction, which any user may
// create where they can create a folder, to a private directory. Walking from
// the root means every parent has been judged before a listing traverses it.
// The volume of the whole path is judged next, then the directory's final
// path is taken once, for openIn to compare each file's with. A missing
// component is accepted, so that the caller reports the directory not found.
// ponytail: a component swapped for a link between the walk and the final
// path is not caught; one swapped after it is, by openIn. Closing that window
// means opening each component by handle relative to its parent
// (NtCreateFile), which syscall does not offer.
func dirCheck(dir string) (string, bool, string) {
	const mountPoint = 0xA0000003 // IO_REPARSE_TAG_MOUNT_POINT, junctions included
	switch {
	case IsUNC(dir):
		return "", false, "network path"
	case !isDriveAbsolute(dir):
		return "", false, "not an absolute path"
	case strings.ContainsAny(dir, `*?<>"`):
		return "", false, "wildcard in the path" // FindFirstFileW would expand it
	}
	// As Windows resolves it: / to \, .. removed; \\?\C:\ is C:\.
	d := filepath.Clean(strings.TrimPrefix(dir, `\\?\`))
	drive, err := syscall.UTF16PtrFromString(d[:3])
	if err != nil {
		return "", false, err.Error()
	}
	if t, _, _ := procGetDriveTypeW.Call(uintptr(unsafe.Pointer(drive))); t != driveFixed {
		return "", false, fmt.Sprintf("%s is not a local fixed drive (drive type %d)", d[:3], t)
	}
	cur := d[:2]
	for _, c := range strings.Split(strings.Trim(d[3:], `\`), `\`) {
		if c == "" {
			continue
		}
		cur += `\` + c
		p, err := syscall.UTF16PtrFromString(cur)
		if err != nil {
			return "", false, err.Error()
		}
		var fd syscall.Win32finddata
		h, err := syscall.FindFirstFile(p, &fd)
		if err == syscall.ERROR_FILE_NOT_FOUND || err == syscall.ERROR_PATH_NOT_FOUND {
			return "", true, ""
		}
		if err != nil {
			return "", false, cur + ": " + err.Error()
		}
		syscall.FindClose(h)
		switch {
		case fd.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT == 0:
		case fd.Reserved0 == mountPoint:
			if why := volumeMount(cur); why != "" {
				return "", false, cur + ": " + why
			}
		case fd.Reserved0 == syscall.IO_REPARSE_TAG_SYMLINK:
			return "", false, cur + ": symbolic link, not followed"
		default:
			return "", false, fmt.Sprintf("%s: reparse point (tag 0x%08X), not followed", cur, fd.Reserved0)
		}
	}
	if ok, why := fixedVolume(d); !ok {
		return "", false, why
	}
	return dirFinal(d)
}

// volumeMount reads the reparse data of a mount point component, opened
// without following it, and refuses it unless isVolumeMount accepts it.
func volumeMount(path string) string {
	const fileReadAttributes = 0x80
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err.Error()
	}
	h, err := syscall.CreateFile(p, fileReadAttributes, fileShareAll, nil,
		syscall.OPEN_EXISTING, syscall.FILE_FLAG_OPEN_REPARSE_POINT|syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "not opened: " + err.Error()
	}
	defer syscall.CloseHandle(h)
	buf := make([]byte, syscall.MAXIMUM_REPARSE_DATA_BUFFER_SIZE)
	var n uint32
	if err := syscall.DeviceIoControl(h, syscall.FSCTL_GET_REPARSE_POINT, nil, 0, &buf[0], uint32(len(buf)), &n, nil); err != nil {
		return "reparse data: " + err.Error()
	}
	if !isVolumeMount(buf[:n]) {
		return "junction, not followed"
	}
	return ""
}

// dirFinal opens a directory dirCheck accepted, following its last component
// as the files under it will be, and returns its final path without a
// trailing separator. A directory gone since the walk is accepted as missing.
func dirFinal(d string) (string, bool, string) {
	const fileReadAttributes = 0x80
	p, err := syscall.UTF16PtrFromString(d)
	if err != nil {
		return "", false, err.Error()
	}
	h, err := syscall.CreateFile(p, fileReadAttributes, fileShareAll, nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err == syscall.ERROR_FILE_NOT_FOUND || err == syscall.ERROR_PATH_NOT_FOUND {
		return "", true, ""
	}
	if err != nil {
		return "", false, "not opened: " + err.Error()
	}
	defer syscall.CloseHandle(h)
	final, err := finalPath(h)
	if err != nil {
		return "", false, err.Error()
	}
	return strings.TrimRight(final, `\`), true, ""
}

// createExclusive creates path for writing with CREATE_NEW, which refuses an
// existing name and, with FILE_FLAG_OPEN_REPARSE_POINT, a link left there, and
// gives the file archiveSDDL's protected DACL for the current user.
func createExclusive(path string) (*os.File, error) {
	t, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		return nil, err
	}
	defer t.Close()
	tu, err := t.GetTokenUser()
	if err != nil {
		return nil, err
	}
	sid, err := tu.User.Sid.String()
	if err != nil {
		return nil, err
	}
	sddl, err := syscall.UTF16PtrFromString(archiveSDDL(sid))
	if err != nil {
		return nil, err
	}
	const sddlRevision1 = 1
	var sd uintptr
	if r, _, err := procConvertStringSecurityDescriptorToSecurityDescriptorW.Call(uintptr(unsafe.Pointer(sddl)), sddlRevision1, uintptr(unsafe.Pointer(&sd)), 0); r == 0 {
		return nil, fmt.Errorf("ConvertStringSecurityDescriptorToSecurityDescriptorW: %w", err)
	}
	defer procLocalFree.Call(sd)
	sa := syscall.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(sa))
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := syscall.CreateFile(p, syscall.GENERIC_WRITE, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE, &sa,
		syscall.CREATE_NEW, syscall.FILE_ATTRIBUTE_NORMAL|syscall.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}
