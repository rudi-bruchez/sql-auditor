package collect

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A nil Debug is every ordinary run, and it reaches exactly the same call sites
// as a set one. Unlike Progress, whose nil default is stderr because
// lastResort's output is sometimes the only surviving record of a run, silence
// is the right default here: nothing on this channel is a record anybody would
// miss.
func TestTheTimelineIsSilentUntilItIsAskedFor(t *testing.T) {
	var o Options
	o.Debugf("this must not reach stderr, and must not panic")
	if o.Debug != nil {
		t.Error("Debugf invented a destination")
	}
}

func TestTheTimelineWritesOneLinePerCall(t *testing.T) {
	var buf bytes.Buffer
	o := Options{Debug: &buf}
	o.Debugf("connecting to %s", "sql01")
	o.Debugf("connected")

	want := "connecting to sql01\nconnected\n"
	if buf.String() != want {
		t.Errorf("got %q, want %q", buf.String(), want)
	}
}

// The complaint the mode answers is "I cannot tell what it is waiting on". The
// answer is only worth anything if the line is written BEFORE the wait: a run
// that hangs prints no "finished" line, so a timeline written after the fact
// goes silent at exactly the moment it was needed.
func TestTheTimelineNamesEachStepBeforeItIsTaken(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "no-such-corpus")
	var buf bytes.Buffer
	// This run fails on the corpus, before a socket is opened — which is the
	// point: everything asserted below is written on the way to that failure.
	_, _ = Run(context.Background(), Options{
		Config: &Config{
			Server:     "localhost",
			OutputDir:  filepath.Join(dir, "output"),
			QueriesDir: missing,
		},
		Corpus: os.DirFS(missing),
		Root:   ".",
		Now:    time.Now(),
		Debug:  &buf,
	})

	for _, want := range []string{
		"probing the output directory",
		"hashing the corpus",
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("the timeline does not mention %q:\n%s", want, buf.String())
		}
	}
}

// A timeline is read by a person who is about to paste it into a mail or a
// ticket. SQL_PASSWORD must not be in it — that would be a worse bug than any
// the mode was added to find — and neither must the count of .env settings
// betray it, which is why the CLI logs the count and never the map.
func TestTheTimelineCarriesNoSecret(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "no-such-corpus")
	var buf bytes.Buffer
	_, _ = Run(context.Background(), Options{
		Config: &Config{
			Server:     "localhost",
			User:       "auditor",
			Password:   "hunter2-in-production",
			OutputDir:  filepath.Join(dir, "output"),
			QueriesDir: missing,
		},
		Corpus: os.DirFS(missing),
		Root:   ".",
		Now:    time.Now(),
		Debug:  &buf,
	})
	if strings.Contains(buf.String(), "hunter2-in-production") {
		t.Errorf("the password is in the timeline:\n%s", buf.String())
	}
}
