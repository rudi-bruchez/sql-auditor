package collect

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// 10.system/021.host-info against a real instance, through runUnit. Skipped
// unless SQL_AUDITOR_LIVE_SERVER is set (liveConfig):
//
//	SQL_AUDITOR_LIVE_SERVER=localhost,1433 SQL_AUDITOR_LIVE_USER=sa \
//	SQL_AUDITOR_LIVE_PASSWORD=... go test ./collect/ -run '^TestLiveHostInfoBuild' -v
//
// The lab runs on Linux, so the Windows strings are handed to the collector
// in place of @@VERSION: the extraction under test is the collector's own,
// not a copy of it. Nothing is created on the instance.

const hostInfoRel = "10.system/021.host-info.sql"

var hostInfoVersionDecl = regexp.MustCompile(`(?m)^DECLARE @version nvarchar\(4000\) = @@VERSION;\r?$`)

// hostInfoScript is 021 as Discover reads it, with @@VERSION replaced by
// version when version is not empty.
func hostInfoScript(t *testing.T, version string) Script {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "queries", filepath.FromSlash(hostInfoRel)))
	if err != nil {
		t.Fatal(err)
	}
	sql := string(b)
	if n := len(hostInfoVersionDecl.FindAllString(sql, -1)); n != 1 {
		t.Fatalf("021 declares @version from @@VERSION %d times on a line of its own, want 1", n)
	}
	if version != "" {
		lit := "N'" + strings.ReplaceAll(version, "'", "''") + "'"
		sql = hostInfoVersionDecl.ReplaceAllLiteralString(sql, "DECLARE @version nvarchar(4000) = "+lit+";")
	}
	s := parseScript(hostInfoRel, sql)
	if s.LintError != "" {
		t.Fatalf("021 does not lint: %s", s.LintError)
	}
	return s
}

// hostInfoRun runs s on master and returns the root document.
func hostInfoRun(t *testing.T, s Script) map[string]any {
	t.Helper()
	cfg := liveConfig(t)
	cfg.Database = "master"
	cfg.QueryTimeout = 30 * time.Second
	ctx := context.Background()
	db, err := Open(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	dir := t.TempDir()
	m, rw := &Manifest{}, newRunWriter(dir, 1<<20)
	if _, err := runUnit(ctx, ctx, conn, Options{Config: cfg}, m, rw, s, DatabaseFolder{}, nil, 0); err != nil {
		t.Fatalf("021: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(ResultRelativePath(s.Dir, s.Base, ""))))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("%v in %s", err, raw)
	}
	return doc
}

// hostBuild is host.build, and whether the key is there at all.
func hostBuild(t *testing.T, doc map[string]any) (any, bool) {
	t.Helper()
	host, ok := doc["host"].(map[string]any)
	if !ok {
		t.Fatalf("no host object in %v", doc)
	}
	v, ok := host["build"]
	return v, ok
}

func TestLiveHostInfoBuild(t *testing.T) {
	// The lab's own @@VERSION: Linux, where a build would be invented.
	t.Run("this instance", func(t *testing.T) {
		doc := hostInfoRun(t, hostInfoScript(t, ""))
		v, present := hostBuild(t, doc)
		t.Logf("platform %v, host.build %v (present %v)", doc["platform"], v, present)
		if doc["platform"] == "Linux" && v != nil {
			t.Errorf("host.build on Linux = %#v, want null or absent", v)
		}
	})
	// Real @@VERSION strings, with and without (Hypervisor), and the shapes
	// that must give nothing.
	for _, c := range []struct {
		name, version string
		want          float64 // 0: null or absent
	}{
		{"2016 on Windows Server 2016, hypervisor", "Microsoft SQL Server 2016 (SP3) (KB5003279) - 13.0.6300.2 (X64) \n\tAug  7 2021 01:20:37 \n\tCopyright (c) Microsoft Corporation\n\tStandard Edition (64-bit) on Windows Server 2016 Datacenter 10.0 <X64> (Build 14393: ) (Hypervisor)\n", 14393},
		{"2019 on Windows Server 2019, hypervisor", "Microsoft SQL Server 2019 (RTM-CU22) (KB5027702) - 15.0.4322.2 (X64) \n\tJul 27 2023 18:11:00 \n\tCopyright (C) 2019 Microsoft Corporation\n\tEnterprise Edition: Core-based Licensing (64-bit) on Windows Server 2019 Datacenter 10.0 <X64> (Build 17763: ) (Hypervisor)\n", 17763},
		{"2022 on Windows Server 2022, bare metal", "Microsoft SQL Server 2022 (RTM-CU12) (KB5033663) - 16.0.4115.5 (X64) \n\tMar  4 2024 08:56:10 \n\tCopyright (C) 2022 Microsoft Corporation\n\tStandard Edition (64-bit) on Windows Server 2022 Standard 10.0 <X64> (Build 20348: ) \n", 20348},
		{"2012 on Windows NT 6.1 with a service pack", "Microsoft SQL Server 2012 (SP4) (KB4018073) - 11.0.7001.0 (X64) \n\tAug 15 2017 10:23:29 \n\tCopyright (c) Microsoft Corporation\n\tStandard Edition (64-bit) on Windows NT 6.1 <X64> (Build 7601: Service Pack 1) (Hypervisor)\n", 7601},
		{"Linux", "Microsoft SQL Server 2025 (RTM-CU5) (KB5084896) - 17.0.4045.5 (X64) \n\tMay 11 2026 19:42:59 \n\tCopyright (C) 2025 Microsoft Corporation\n\tEnterprise Developer Edition (64-bit) on Linux (Ubuntu 24.04.4 LTS) <X64>", 0},
		{"Windows without a build", "Microsoft SQL Server 2016 - 13.0.1601.5 (X64) on Windows Server 2016 Datacenter 10.0 <X64>", 0},
		// Synthetic: no real Linux @@VERSION has a build. It pins that the
		// number is read only in a sentence that says Windows.
		{"Build outside a Windows sentence", "Microsoft SQL Server 2025 - 17.0.4045.5 (X64) on Linux (Ubuntu 24.04.4 LTS) <X64> (Build 9999: )", 0},
		{"Build without digits", "Microsoft SQL Server 2016 - 13.0.1601.5 (X64) on Windows Server 2016 Datacenter 10.0 <X64> (Build : )", 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			v, _ := hostBuild(t, hostInfoRun(t, hostInfoScript(t, c.version)))
			if c.want == 0 {
				if v != nil {
					t.Errorf("host.build = %#v, want null or absent", v)
				}
				return
			}
			if f, ok := v.(float64); !ok || f != c.want {
				t.Errorf("host.build = %#v, want %v", v, c.want)
			}
		})
	}
}
