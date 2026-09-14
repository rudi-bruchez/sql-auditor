package collect

import (
	"fmt"
	"slices"
	"sort"
	"strings"
)

// ProfileSkipReason is the one place the sentence is built, so MANIFEST.txt
// can recognise these skips by equality rather than by searching the text.
func ProfileSkipReason(profile string) string {
	return "not in profile " + profile
}

// inProfile reports whether a script belongs to the profile. An empty profile
// is the whole corpus.
func inProfile(s Script, profile string) bool {
	return profile == "" || slices.Contains(s.Profiles, profile)
}

// WideningPurposes returns the @widened purposes of the planned scripts that
// will run, that is, those planScripts did not skip.
func WideningPurposes(plan []plannedScript) map[string]bool {
	out := map[string]bool{}
	for _, p := range plan {
		if p.Skip == "" && p.Script.LintError == "" && p.Script.Widened != "" {
			out[p.Script.Widened] = true
		}
	}
	return out
}

// membershipPurposes is the fallback when no plan can be built because the
// version probe failed: the purposes of the lint-clean scripts in the profile
// whose flag, if any, is on. It is the most a selection without a version can
// know.
func membershipPurposes(scripts []Script, profile string, flags map[string]bool) map[string]bool {
	out := map[string]bool{}
	for _, s := range scripts {
		if s.LintError != "" || s.Widened == "" || !inProfile(s, profile) {
			continue
		}
		if s.RequiresFlag != "" && !flags[s.RequiresFlag] {
			continue
		}
		out[s.Widened] = true
	}
	return out
}

// ProfileMembers returns the lint-clean scripts that declare the profile, or
// scripts unchanged when no profile is requested.
func ProfileMembers(scripts []Script, profile string) []Script {
	if profile == "" {
		return scripts
	}
	var out []Script
	for _, s := range scripts {
		if s.LintError == "" && slices.Contains(s.Profiles, profile) {
			out = append(out, s)
		}
	}
	return out
}

// CheckProfile refuses the profile and flag combinations that would produce a
// run which looks successful and collects nothing the operator asked for. It
// judges declarations and lint only. A member that the instance's version or
// the login's rights will gate off is not known here; it is skipped at planning
// and recorded in skipped_scripts, as a flagged collector too recent for the
// instance is today.
func CheckProfile(scripts []Script, profile string, flags map[string]bool) error {
	if profile == "" {
		return nil
	}
	if _, ok := KnownProfiles[profile]; !ok {
		return fmt.Errorf("unknown profile %q; expected one of %s",
			profile, strings.Join(knownProfileNames(), ", "))
	}
	var members, failed []Script
	for _, s := range scripts {
		if !slices.Contains(s.Profiles, profile) {
			continue
		}
		if s.LintError != "" {
			failed = append(failed, s)
			continue
		}
		members = append(members, s)
	}
	if len(members) == 0 {
		if len(failed) > 0 {
			return fmt.Errorf("profile %s has no usable collector in this corpus: %s",
				profile, lintFailures(failed))
		}
		return fmt.Errorf("profile %s has no collector in this corpus; "+
			"a corpus exported before profiles existed declares none", profile)
	}
	names := make([]string, 0, len(flags))
	for name, on := range flags {
		if on {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		if slices.ContainsFunc(members, func(s Script) bool { return s.RequiresFlag == name }) {
			continue
		}
		option := KnownFlags[name]
		if option == "" {
			option = name
		}
		var gatedFailed []Script
		for _, s := range failed {
			if s.RequiresFlag == name {
				gatedFailed = append(gatedFailed, s)
			}
		}
		if len(gatedFailed) > 0 {
			return fmt.Errorf("%s has no usable collector in profile %s: %s",
				option, profile, lintFailures(gatedFailed))
		}
		return fmt.Errorf("%s has no collector in profile %s, so it would collect nothing; "+
			"drop the option or the profile", option, profile)
	}
	return nil
}

// profileCounts returns how many lint-clean scripts declare the profile (0
// without one) and how many lint-clean scripts the corpus holds.
func profileCounts(scripts []Script, profile string) (members, corpus int) {
	for _, s := range scripts {
		if s.LintError != "" {
			continue
		}
		corpus++
		if profile != "" && slices.Contains(s.Profiles, profile) {
			members++
		}
	}
	return members, corpus
}

func lintFailures(scripts []Script) string {
	parts := make([]string, 0, len(scripts))
	for _, s := range scripts {
		parts = append(parts, fmt.Sprintf("%s failed lint (%s)", s.Path, s.LintError))
	}
	return strings.Join(parts, "; ")
}
