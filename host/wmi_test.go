package host

import (
	"bytes"
	"os"
	"regexp"
	"strings"
	"testing"
	"unicode/utf16"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestScriptsAreVisibleAndPlain(t *testing.T) {
	bad := []*regexp.Regexp{
		regexp.MustCompile(`(?i)-EncodedCommand`),
		regexp.MustCompile(`(?i)-enc`),
		regexp.MustCompile(`(?i)Invoke-Expression`),
		regexp.MustCompile(`(?i)\biex\b`),
		regexp.MustCompile(`[A-Za-z0-9+/=]{40,}`),
	}
	for name, s := range map[string]string{"VSSScript": VSSScript, "ClusterScript": ClusterScript} {
		for _, re := range bad {
			if m := re.FindString(s); m != "" {
				t.Errorf("%s holds %q (%s)", name, m, re)
			}
		}
	}
}

func TestScriptsSetUTF8WithoutBOM(t *testing.T) {
	const head = "[Console]::OutputEncoding=New-Object Text.UTF8Encoding $false;"
	for name, s := range map[string]string{"VSSScript": VSSScript, "ClusterScript": ClusterScript} {
		if !strings.HasPrefix(s, head) {
			t.Errorf("%s does not start with %q", name, head)
		}
	}
}

func TestParseVSS(t *testing.T) {
	v, err := ParseVSS(readFixture(t, "wmi-vss.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Shadows) != 1 || len(v.Storage) != 1 || len(v.Providers) != 1 || len(v.Volumes) != 1 {
		t.Fatalf("got %d shadows, %d storage, %d providers, %d volumes, want one each",
			len(v.Shadows), len(v.Storage), len(v.Providers), len(v.Volumes))
	}
	s := v.Shadows[0]
	if !s.Persistent || !s.ClientAccessible || !s.NoAutoRelease || s.State != 12 {
		t.Errorf("shadow = %+v, want persistent, client accessible, no auto release, state 12", s)
	}
	if s.InstallDate != "2026-10-20T08:15:02.0000000Z" {
		t.Errorf("InstallDate = %q", s.InstallDate)
	}
	if s.VolumeName != v.Volumes[0].DeviceID || v.Volumes[0].Name != `C:\` {
		t.Errorf("shadow volume %q does not map to C:\\ (%+v)", s.VolumeName, v.Volumes[0])
	}
	if p := v.Providers[0]; p.ID != "{B5946137-7B9F-4925-AF80-51ABD60B20D5}" || p.ID != s.ProviderID {
		t.Errorf("provider = %+v, shadow provider %q", p, s.ProviderID)
	}
	if st := v.Storage[0]; st.UsedSpace != 475136 || st.AllocatedSpace != 335544320 || st.MaxSpace != 6442450944 {
		t.Errorf("storage = %+v", st)
	}
}

func TestParseVSSSingleItemIsArray(t *testing.T) {
	b := []byte(`{"shadows":{"ID":"{6F1C2A3B-4D5E-4F60-8A7B-9C0D1E2F3A4B}","State":12},"storage":[],"providers":[],"volumes":[]}`)
	_, err := ParseVSS(b)
	if err == nil || !strings.Contains(err.Error(), "@()") {
		t.Errorf("err = %v, want an error naming @()", err)
	}
}

func TestParseVSSStripsBOM(t *testing.T) {
	b := append([]byte("\xef\xbb\xbf"), readFixture(t, "wmi-vss.json")...)
	if v, err := ParseVSS(b); err != nil || len(v.Shadows) != 1 {
		t.Errorf("got %d shadows, err %v", len(v.Shadows), err)
	}
}

func TestParseClusterNodeMajority(t *testing.T) {
	c, err := ParseCluster(readFixture(t, "wmi-cluster.json"))
	if err != nil {
		t.Fatal(err)
	}
	if c.Absent || c.Cluster == nil || c.Cluster.Name != "cluster" || c.Cluster.QuorumType != "Node Majority" {
		t.Fatalf("cluster = %+v", c)
	}
	if c.Quorum != nil {
		t.Errorf("quorum = %+v, want none under node majority", c.Quorum)
	}
	if len(c.Nodes) != 2 || c.Nodes[0].Name != "SQL01" || c.Nodes[1].Name != "SQL02" {
		t.Errorf("nodes = %+v", c.Nodes)
	}
	if len(c.Groups) != 1 || c.Groups[0].OwnerNode != "SQL01" {
		t.Errorf("groups = %+v", c.Groups)
	}
	if len(c.Resources) != 1 {
		t.Fatalf("resources = %+v", c.Resources)
	}
	r := c.Resources[0]
	if r.Type != "SQL Server Availability Group" || r.OwnerGroup != "AG1" {
		t.Errorf("resource = %+v", r)
	}
	for k, want := range map[string]string{"LeaseTimeout": "20000", "HealthCheckTimeout": "30000", "FailureConditionLevel": "3"} {
		if got := string(r.PrivateProperties[k]); got != want {
			t.Errorf("%s = %q, want %s", k, got, want)
		}
	}
}

func TestParseClusterAbsent(t *testing.T) {
	c, err := ParseCluster(readFixture(t, "wmi-absent.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Absent || c.Native != "InvalidNamespace" || c.Cluster != nil {
		t.Errorf("got %+v, want absent, InvalidNamespace", c)
	}
}

func TestParseClusterRefusesNonUTF8(t *testing.T) {
	b := readFixture(t, "wmi-cluster.json")
	c, err := ParseCluster(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Networks) != 1 || c.Networks[0].Name != "Réseau de cluster 1" {
		t.Errorf("networks = %+v, want Réseau de cluster 1", c.Networks)
	}
	cp850 := bytes.Replace(b, []byte("Réseau"), []byte("R\x82seau"), 1)
	if bytes.Equal(cp850, b) {
		t.Fatal("fixture holds no Réseau to re-encode")
	}
	if _, err := ParseCluster(cp850); err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Errorf("err = %v, want an error naming UTF-8", err)
	}
}

func TestSQLWriterLog(t *testing.T) {
	const def = `C:\Program Files\Microsoft SQL Server\90\Shared`
	lab := "[Logging]\r\nEnableTracing=1\r\nTraceLevel=DEFAULT\r\nTraceFile=C:\\Program Files\\Microsoft SQL Server\\90\\Shared\\SqlWriterLogger.txt\r\nTraceFileSizeMb=1\r\nForceFlush=0\r\n"
	utf16le := func(s string) []byte {
		b := []byte{0xFF, 0xFE}
		for _, c := range utf16.Encode([]rune(s)) {
			b = append(b, byte(c), byte(c>>8))
		}
		return b
	}
	const custom = "[Logging]\r\nTraceFile=D:\\logs\\w.txt\r\n"
	cases := []struct {
		name           string
		ini            []byte
		dir, stem, ext string
	}{
		{"lab", []byte(lab), def, "SqlWriterLogger", ".txt"},
		{"lab UTF-16LE", utf16le(lab), def, "SqlWriterLogger", ".txt"},
		{"custom", []byte(custom), `D:\logs`, "w", ".txt"},
		// The lab's TraceFile is the default path, so only a custom one shows
		// that UTF-16LE is decoded at all.
		{"custom UTF-16LE", utf16le(custom), `D:\logs`, "w", ".txt"},
		{"commented", []byte("[Logging]\n; TraceFile=D:\\logs\\w.txt\n"), def, "SqlWriterLogger", ".txt"},
		{"other section", []byte("[Other]\nTraceFile=D:\\logs\\w.txt\n"), def, "SqlWriterLogger", ".txt"},
		{"empty", nil, def, "SqlWriterLogger", ".txt"},
	}
	for _, c := range cases {
		dir, stem, ext := SQLWriterLog(c.ini, def)
		if dir != c.dir || stem != c.stem || ext != c.ext {
			t.Errorf("%s: got %q %q %q, want %q %q %q", c.name, dir, stem, ext, c.dir, c.stem, c.ext)
		}
	}
}

func TestIsSQLWriterLog(t *testing.T) {
	for name, want := range map[string]bool{
		"SqlWriterLogger.txt":     true,
		"sqlwriterlogger1.TXT":    true,
		"SqlWriterLogger12.txt":   true,
		"SqlWriterLogger.txt.bak": false,
		"SqlWriterConfig.ini":     false,
		"SqlWriterLoggerx.txt":    false,
	} {
		if got := IsSQLWriterLog(name, "SqlWriterLogger", ".txt"); got != want {
			t.Errorf("IsSQLWriterLog(%q) = %v, want %v", name, got, want)
		}
	}
}
