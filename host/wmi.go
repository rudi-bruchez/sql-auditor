package host

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// scriptHead starts every script. Windows PowerShell 5.1 writes redirected
// output in the OEM code page unless told otherwise, and encoding/json would
// turn those bytes into U+FFFD without an error; the encoding is built with
// $false because [Text.Encoding]::UTF8 writes a BOM.
const scriptHead = `[Console]::OutputEncoding=New-Object Text.UTF8Encoding $false; $ProgressPreference='SilentlyContinue'; $ErrorActionPreference='Stop'
`

// VSSScript prints one JSON object: the shadow copies, their storage, the
// providers and the volumes the shadow copies are mapped through. Every
// collection is wrapped in @() so that one item stays an array. The scripts
// hold no double quote, which powershell.exe -Command mangles.
const VSSScript = scriptHead + `function Iso($t) { if ($t) { $t.ToUniversalTime().ToString('o') } }
[ordered]@{
  shadows = @(Get-CimInstance Win32_ShadowCopy | ForEach-Object { [ordered]@{
    ID = $_.ID; SetID = $_.SetID; VolumeName = $_.VolumeName; DeviceObject = $_.DeviceObject
    InstallDate = Iso $_.InstallDate; ProviderID = $_.ProviderID; Persistent = $_.Persistent
    ClientAccessible = $_.ClientAccessible; NoAutoRelease = $_.NoAutoRelease; State = $_.State } })
  storage = @(Get-CimInstance Win32_ShadowStorage | ForEach-Object { [ordered]@{
    Volume = $_.Volume.DeviceID; DiffVolume = $_.DiffVolume.DeviceID
    UsedSpace = $_.UsedSpace; AllocatedSpace = $_.AllocatedSpace; MaxSpace = $_.MaxSpace } })
  providers = @(Get-CimInstance Win32_ShadowProvider | ForEach-Object { [ordered]@{
    Name = $_.Name; ID = $_.ID; Version = $_.Version } })
  volumes = @(Get-CimInstance Win32_Volume | ForEach-Object { [ordered]@{
    DeviceID = $_.DeviceID; DriveLetter = $_.DriveLetter; Name = $_.Name; Label = $_.Label; DriveType = $_.DriveType } })
} | ConvertTo-Json -Depth 5 -Compress
`

// ClusterScript prints the cluster's settings, or {"absent":true,...} when
// root\MSCluster does not answer. NativeErrorCode is not localized:
// InvalidNamespace on a host that is not a node. The quorum resource is null
// under node majority.
const ClusterScript = scriptHead + `try {
  $ns = 'root\MSCluster'
  $c = Get-CimInstance -Namespace $ns -ClassName MSCluster_Cluster
  $q = @(Get-CimAssociatedInstance -InputObject $c -Association MSCluster_ClusterToQuorumResource)
  [ordered]@{
    cluster = [ordered]@{
      Name = $c.Name; QuorumType = $c.QuorumType
      SameSubnetDelay = $c.SameSubnetDelay; SameSubnetThreshold = $c.SameSubnetThreshold
      CrossSubnetDelay = $c.CrossSubnetDelay; CrossSubnetThreshold = $c.CrossSubnetThreshold
      RouteHistoryLength = $c.RouteHistoryLength; QuorumArbitrationTimeMax = $c.QuorumArbitrationTimeMax }
    quorum = $(if ($q.Count) { [ordered]@{ Name = $q[0].Name; Type = $q[0].Type } } else { $null })
    nodes = @(Get-CimInstance -Namespace $ns -ClassName MSCluster_Node | ForEach-Object { [ordered]@{
      Name = $_.Name; State = $_.State; NodeWeight = $_.NodeWeight } })
    networks = @(Get-CimInstance -Namespace $ns -ClassName MSCluster_Network | ForEach-Object { [ordered]@{
      Name = $_.Name; Role = $_.Role; Address = $_.Address; AddressMask = $_.AddressMask; State = $_.State } })
    groups = @(Get-CimInstance -Namespace $ns -ClassName MSCluster_ResourceGroup | ForEach-Object { [ordered]@{
      Name = $_.Name; State = $_.State; OwnerNode = $_.OwnerNode
      FailoverThreshold = $_.FailoverThreshold; FailoverPeriod = $_.FailoverPeriod } })
    resources = @(Get-CimInstance -Namespace $ns -ClassName MSCluster_Resource | ForEach-Object {
      $p = [ordered]@{}
      foreach ($x in $_.PrivateProperties.CimInstanceProperties) { $p[$x.Name] = $x.Value }
      [ordered]@{
        Name = $_.Name; Type = $_.Type; State = $_.State; OwnerGroup = $_.OwnerGroup
        RestartAction = $_.RestartAction; RestartThreshold = $_.RestartThreshold; RestartPeriod = $_.RestartPeriod
        RetryPeriodOnFailure = $_.RetryPeriodOnFailure; PendingTimeout = $_.PendingTimeout; PrivateProperties = $p } })
  } | ConvertTo-Json -Depth 5 -Compress
} catch [Microsoft.Management.Infrastructure.CimException] {
  [ordered]@{ absent = $true; native = $_.Exception.NativeErrorCode.ToString() } | ConvertTo-Json -Compress
}
`

// VSSInfo is what VSSScript prints. Volumes map a shadow copy's VolumeName
// (a \\?\Volume{...}\ path) to its drive letter or mount point.
type VSSInfo struct {
	Shadows   []Shadow         `json:"shadows"`
	Storage   []ShadowStorage  `json:"storage"`
	Providers []ShadowProvider `json:"providers"`
	Volumes   []Volume         `json:"volumes"`
}

// Shadow is one Win32_ShadowCopy.
type Shadow struct {
	ID, SetID, VolumeName, DeviceObject string
	InstallDate                         string // UTC, ISO 8601
	ProviderID                          string
	Persistent, ClientAccessible        bool
	NoAutoRelease                       bool
	State                               int
}

// ShadowStorage is one Win32_ShadowStorage, its volumes as device ids.
type ShadowStorage struct {
	Volume, DiffVolume                  string
	UsedSpace, AllocatedSpace, MaxSpace uint64
}

// ShadowProvider is one Win32_ShadowProvider.
type ShadowProvider struct {
	Name, ID, Version string
}

// Volume is one Win32_Volume.
type Volume struct {
	DeviceID, DriveLetter, Name, Label string
	DriveType                          int
}

// ClusterInfo is what ClusterScript prints. Quorum is nil when there is no
// quorum resource, as under node majority.
type ClusterInfo struct {
	Absent    bool              `json:"absent"`
	Native    string            `json:"native,omitempty"` // the CIM error that made it absent
	Cluster   *Cluster          `json:"cluster"`
	Quorum    *QuorumResource   `json:"quorum"`
	Nodes     []ClusterNode     `json:"nodes"`
	Networks  []ClusterNetwork  `json:"networks"`
	Groups    []ClusterGroup    `json:"groups"`
	Resources []ClusterResource `json:"resources"`
}

// Cluster is MSCluster_Cluster.
type Cluster struct {
	Name, QuorumType                             string
	SameSubnetDelay, SameSubnetThreshold         int64
	CrossSubnetDelay, CrossSubnetThreshold       int64
	RouteHistoryLength, QuorumArbitrationTimeMax int64
}

// QuorumResource is the resource MSCluster_ClusterToQuorumResource returns.
type QuorumResource struct {
	Name, Type string
}

// ClusterNode is one MSCluster_Node.
type ClusterNode struct {
	Name              string
	State, NodeWeight int64
}

// ClusterNetwork is one MSCluster_Network.
type ClusterNetwork struct {
	Name                 string
	Role                 int64
	Address, AddressMask string
	State                int64
}

// ClusterGroup is one MSCluster_ResourceGroup.
type ClusterGroup struct {
	Name                              string
	State                             int64
	OwnerNode                         string
	FailoverThreshold, FailoverPeriod int64
}

// ClusterResource is one MSCluster_Resource. PrivateProperties keeps each
// value as WMI gave it, since its type depends on the resource type.
type ClusterResource struct {
	Name, Type                                     string
	State                                          int64
	OwnerGroup                                     string
	RestartAction, RestartThreshold, RestartPeriod int64
	RetryPeriodOnFailure, PendingTimeout           int64
	PrivateProperties                              map[string]json.RawMessage
}

// ParseVSS reads VSSScript's output.
func ParseVSS(b []byte) (VSSInfo, error) {
	var v VSSInfo
	return v, decodeScript(b, &v)
}

// ParseCluster reads ClusterScript's output.
func ParseCluster(b []byte) (ClusterInfo, error) {
	var c ClusterInfo
	return c, decodeScript(b, &c)
}

// decodeScript refuses output that is not UTF-8 rather than letting
// encoding/json replace its bytes, and names the missing @() when a
// collection came back as a single object.
func decodeScript(b []byte, v any) error {
	b = bytes.TrimPrefix(b, []byte("\xef\xbb\xbf"))
	if !utf8.Valid(b) {
		return errors.New("PowerShell output is not UTF-8: [Console]::OutputEncoding was not applied")
	}
	err := json.Unmarshal(b, v)
	var te *json.UnmarshalTypeError
	if errors.As(err, &te) && te.Value == "object" && strings.HasPrefix(te.Type.String(), "[]") {
		return fmt.Errorf("%s is one object, not an array: the script must wrap the collection in @(): %w", te.Field, err)
	}
	return err
}

// SQLWriterLog returns where SQL Writer writes its log: TraceFile in the
// [Logging] section of SqlWriterConfig.ini when set, else
// defaultDir\SqlWriterLogger.txt. The ini is read as UTF-16LE when it starts
// with FF FE, else as UTF-8. Paths are split on \ and / so that this runs the
// same away from Windows.
func SQLWriterLog(ini []byte, defaultDir string) (dir, stem, ext string) {
	dir, stem, ext = defaultDir, "SqlWriterLogger", ".txt"
	text := string(bytes.TrimPrefix(ini, []byte("\xef\xbb\xbf")))
	if len(ini) >= 2 && ini[0] == 0xFF && ini[1] == 0xFE {
		u := make([]uint16, (len(ini)-2)/2)
		for i := range u {
			u[i] = uint16(ini[2+2*i]) | uint16(ini[3+2*i])<<8
		}
		text = string(utf16.Decode(u))
	}
	section := ""
	for line := range strings.SplitSeq(text, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = line
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		v = strings.TrimSpace(v)
		if !ok || v == "" || !strings.EqualFold(section, "[Logging]") || !strings.EqualFold(strings.TrimSpace(k), "TraceFile") {
			continue
		}
		base := v
		if i := strings.LastIndexAny(v, `\/`); i >= 0 {
			dir, base = v[:i], v[i+1:]
		}
		stem, ext = base, ""
		if i := strings.LastIndex(base, "."); i > 0 {
			stem, ext = base[:i], base[i:]
		}
	}
	return dir, stem, ext
}

// IsSQLWriterLog reports whether name is stem+ext or a rollover of it,
// stem+digits+ext, ignoring case.
func IsSQLWriterLog(name, stem, ext string) bool {
	if len(name) < len(stem)+len(ext) ||
		!strings.EqualFold(name[:len(stem)], stem) || !strings.EqualFold(name[len(name)-len(ext):], ext) {
		return false
	}
	for _, c := range name[len(stem) : len(name)-len(ext)] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
