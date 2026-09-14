package collect

import "slices"

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
