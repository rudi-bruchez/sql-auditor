package host

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"
)

// kernel32 is the one DLL loaded by name: Go loads it itself, from the system
// directory. Every other DLL goes through systemDLL, since the process is
// elevated and a bare name would let a DLL left beside the executable be
// loaded instead (see syscall.LoadDLL).
var (
	kernel32                      = syscall.NewLazyDLL("kernel32.dll")
	procGetSystemDirectoryW       = kernel32.NewProc("GetSystemDirectoryW")
	procGetComputerNameExW        = kernel32.NewProc("GetComputerNameExW")
	procGetFinalPathNameByHandleW = kernel32.NewProc("GetFinalPathNameByHandleW")
	procGetVolumePathNameW        = kernel32.NewProc("GetVolumePathNameW")
	procGetDriveTypeW             = kernel32.NewProc("GetDriveTypeW")
	procLocalFree                 = kernel32.NewProc("LocalFree")

	advapi32                  = systemDLL("advapi32.dll")
	procOpenSCManagerW        = advapi32.NewProc("OpenSCManagerW")
	procEnumServicesStatusExW = advapi32.NewProc("EnumServicesStatusExW")
	procCloseServiceHandle    = advapi32.NewProc("CloseServiceHandle")

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

// systemDLL loads name by its absolute path in the system directory.
func systemDLL(name string) *syscall.LazyDLL {
	return syscall.NewLazyDLL(filepath.Join(systemDir(), name))
}

// WindowsPlatform reads this machine, and the instance through inst.
func WindowsPlatform(inst *Instance) Platform {
	return Platform{
		Check:    checkElevated,
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
		PathCheck:    localFixed,
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

// powerShell runs a script in Windows PowerShell by its absolute path, as a
// visible command line, never -EncodedCommand.
func powerShell(ctx context.Context, script string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	exe := filepath.Join(systemDir(), `WindowsPowerShell\v1.0\powershell.exe`)
	out, err := exec.CommandContext(ctx, exe, "-NoProfile", "-NonInteractive", "-Command", script).Output()
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

// localFixed accepts a file whose final path, links resolved, lies on a local
// fixed volume. The volume is the one GetVolumePathNameW returns, so a mount
// point on a local disk is judged as its own volume.
func localFixed(path string) (bool, string) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return false, err.Error()
	}
	const fileReadAttributes = 0x80
	h, err := syscall.CreateFile(p, fileReadAttributes, syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE, nil, syscall.OPEN_EXISTING, syscall.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return false, "not opened: " + err.Error()
	}
	defer syscall.CloseHandle(h)
	buf := make([]uint16, syscall.MAX_PATH)
	for {
		n, _, err := procGetFinalPathNameByHandleW.Call(uintptr(h), uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0)
		if n == 0 {
			return false, "final path: " + err.Error()
		}
		if int(n) < len(buf) {
			buf = buf[:n]
			break
		}
		buf = make([]uint16, n)
	}
	final := syscall.UTF16ToString(buf)
	if strings.HasPrefix(final, `\\?\UNC\`) {
		return false, "network path " + final
	}
	final = strings.TrimPrefix(final, `\\?\`)
	fp, err := syscall.UTF16PtrFromString(final)
	if err != nil {
		return false, err.Error()
	}
	root := make([]uint16, len(final)+2)
	if r, _, err := procGetVolumePathNameW.Call(uintptr(unsafe.Pointer(fp)), uintptr(unsafe.Pointer(&root[0])), uintptr(len(root))); r == 0 {
		return false, "volume of " + final + ": " + err.Error()
	}
	const driveFixed = 3
	if t, _, _ := procGetDriveTypeW.Call(uintptr(unsafe.Pointer(&root[0]))); t != driveFixed {
		return false, fmt.Sprintf("%s is not on a local fixed drive (drive type %d)", final, t)
	}
	return true, ""
}
