package host

import (
	"strings"

	"github.com/rudi-bruchez/sql-auditor/collect"
)

// SourceInput is what decides the instance's event source, the provider name
// its events are logged under.
type SourceInput struct {
	Answered      bool     // the instance answered
	MachineMatch  bool     // and on this machine
	InstanceName  *string  // SERVERPROPERTY('InstanceName'); nil is NULL
	EnvServer     string   // the server string from .env or --server
	LocalServices []string // service names, every state
}

// ChooseEventSource returns MSSQLSERVER, MSSQL$<NAME> or "" when undecided,
// and the rule that decided it. The rules are tried in this order: the
// instance's own name when it answered on this machine, the instance named by
// the server string, the only SQL Server service on the machine. The last two
// return the service's own spelling, since it goes into Provider[@Name=...].
func ChooseEventSource(in SourceInput) (source, rule string) {
	if in.Answered && in.MachineMatch {
		if in.InstanceName == nil {
			return "MSSQLSERVER", "instance"
		}
		return "MSSQL$" + *in.InstanceName, "instance"
	}
	var engines []string
	for _, s := range in.LocalServices {
		if strings.EqualFold(s, "MSSQLSERVER") || (len(s) > 6 && strings.EqualFold(s[:6], "MSSQL$")) {
			engines = append(engines, s)
		}
	}
	// An unparseable server string names no instance and falls to the last rule.
	if name, err := collect.ServerInstance(in.EnvServer); err == nil && name != "" {
		for _, s := range engines {
			if strings.EqualFold(s, "MSSQL$"+name) {
				return s, "env"
			}
		}
		return "", "undecided: no local service MSSQL$" + name
	}
	switch len(engines) {
	case 1:
		return engines[0], "single service"
	case 0:
		return "", "undecided: no SQL Server service on this machine"
	}
	return "", "undecided: several SQL Server services (" + strings.Join(engines, ", ") + ")"
}
