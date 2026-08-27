package tui

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/rudi-bruchez/sql-auditor/collect"
	"github.com/rudi-bruchez/sql-auditor/tui/screen"
)

// tickInterval is how often the activity indicator moves. Fast enough to read
// as motion — the whole point is that a screen waiting on a frozen instance
// must not look like a frozen program — and slow enough that a full repaint of
// a screenful of text over an RDP link costs nothing anybody notices.
const tickInterval = 200 * time.Millisecond

// Run is the wizard. It takes the terminal, drives the four screens, and
// returns the process's exit code.
//
// The main goroutine NEVER enters collect.Run. That function is entirely
// synchronous, and a main loop sitting inside it would read no keystroke at
// all: raw mode delivers ctrl-c as the byte 0x03 in the input stream rather
// than as a signal, so a wizard blocked on the collection could not even be
// interrupted. The collection therefore runs in its own goroutine, as do the
// connection, the probe, the keyboard and the ticker, and everything they have
// to say arrives as an event on one channel that this goroutine drains.
func Run(o collect.Options, in, out *os.File) int {
	t, err := screen.Open(in, out)
	if err != nil {
		// Failing rather than degrading is screen.Open's decision, and this is
		// where it is paid for. The operator is not left without a tool: the
		// non-interactive path is a first-class one and does the same work.
		fmt.Fprintf(os.Stderr, "%v\nrun `sql-auditor collect` instead: it needs no terminal.\n", err)
		return 2
	}
	r := &runner{
		opts:     o,
		events:   make(chan event),
		done:     make(chan struct{}),
		progress: &syncBuffer{},
	}

	// Posted immediately after Open, and inside Run rather than in main: this
	// is the only function in the program that puts a terminal into raw mode,
	// so its defer covers everything it calls — including a panic on the main
	// goroutine — and a failure before this line leaves a perfectly ordinary
	// terminal behind. Close is safe to call twice, which is what lets
	// flushOnExit call it again on the way out: it restores the terminal before
	// emptying the progress buffer, so anything written to stderr afterwards is
	// readable.
	//
	// The buffer is emptied in the SAME defer, after the restore, and that is
	// the whole reason this is not two statements at the end of the function. A
	// panic in Render, in Draw or in stateChanged unwinds straight through
	// here, and an in-line flush would be skipped — losing the buffer, which
	// when lastResort has fired is the only copy of the manifest in existence.
	// A run that leaves no trace at all is what the entire fallback chain
	// exists to prevent.
	defer r.flushOnExit(t, o.Config.OutputDir, o.Now)

	// Every collect writer in this process goes to the buffer, never to the
	// terminal: a "connection lost; attempting one reconnect" landing in the
	// middle of a frame would corrupt the screen. The buffer is emptied to a
	// file on the way out, because for one failure mode — lastResort, when no
	// filesystem will take the manifest — it holds the only copy of the run.
	r.opts.Progress = r.progress

	go r.readKeys(t)
	r.watchSignals()

	s := initialState(o, t.ASCIIOnly())
	code := loop(r.events, func(lines []string) { _ = t.Draw(lines) }, t.Size, s, r.stateChanged)

	// From here on nothing may block on a send. done releases every producer
	// that goes through r.send; the drain releases the observer, whose sends go
	// straight to the channel by design — it applies back pressure on a
	// collection producing events faster than the screen absorbs them, which is
	// right while the loop is running and a deadlock the moment it is not.
	close(r.done)
	r.stopTicker()
	r.stopWork()
	go func() {
		for range r.events {
		}
	}()

	return code
}

// flushOnExit is the whole of Run's exit path, and it is a function of its own
// so that it can be deferred as one thing and tested as one thing.
//
// The order is fixed: the terminal is restored FIRST, because the final frame
// is erased by Close and a run log path written before that would be painted
// over on a screen still in raw mode — the one place the operator will not read
// it.
func (r *runner) flushOnExit(t *screen.Terminal, outputDir string, now time.Time) {
	_ = t.Close()
	path, err := flushProgress(r.progress.snapshot(), outputDir, now)
	switch {
	case err != nil:
		// spill has already put the content on stderr; saying where it failed
		// is what turns that wall of text into something actionable.
		fmt.Fprintf(os.Stderr, "the run log above could not be written to %s\n", outputDir)
	case path != "":
		fmt.Fprintf(os.Stderr, "run log: %s\n", path)
	}
}

// runner owns everything the wizard starts and has to stop. Every field is
// read and written from the loop's goroutine only — stateChanged is called from
// there, and so is the exit path — which is why there is no mutex on any of
// them. The one shared thing is the progress buffer, and it carries its own.
type runner struct {
	opts   collect.Options
	events chan event
	// done is closed once the loop has returned. It is the sole shutdown
	// mechanism for the producers that use send; the keyboard reader does not
	// need it, since Close on the terminal is what unblocks its Read.
	done chan struct{}

	progress *syncBuffer

	// stopTick and stopJob are the cancels of the ticker and of whatever work
	// the current step started. Nil when nothing is running.
	stopTick context.CancelFunc
	stopJob  context.CancelFunc
}

// send is how every goroutine but the observer talks to the loop. The select
// on done is what guarantees no producer can be left blocked on a channel that
// has lost its reader: a ticker still sending into a wizard that has quit would
// be a goroutine leaked for the life of the process.
func (r *runner) send(e event) {
	select {
	case r.events <- e:
	case <-r.done:
	}
}

// guard converts a panic into an event. recover is PER GOROUTINE: without one
// of these in each of them, a panic in the ticker would end the process without
// running a single defer — leaving the terminal in raw mode, the operator with
// a shell that echoes nothing, and the run log unwritten.
func (r *runner) guard(where string) {
	if v := recover(); v != nil {
		r.send(panicEvent{value: v, where: where})
	}
}

// collectionJob names the goroutine in both places that have to agree on it:
// startWork passes it to the generic guard, and guardCollection used to spell
// the same words a second time. The parameter is in fact dead at that call site
// — guardCollection consumes the panic first — but two spellings of one name is
// a divergence waiting to happen in a bug report nobody can act on.
const collectionJob = "the collection"

// guardCollection is guard for the one goroutine whose screen waits on a second
// event before it will let go. StepCollecting ends on collectDoneEvent and on
// nothing else — deliberately, since a panic elsewhere in the wizard must not
// cut a run short while collect.Run is still writing its manifest and building
// its archive. But a panic INSIDE collect.Run kills the goroutine at the panic,
// so the send at the end of collect() is never reached and that event never
// comes: the screen stayed on "stopping…" for ever, Ctrl-C only re-set the flag
// it had already set, and the way out was kill -9 with the terminal in raw mode.
//
// So the panic reports itself and then reports the run as over, in that order:
// the first sets Stopping and counts the error, the second releases the screen.
// The code is 2 for the same reason panicEvent uses it — the run happened and
// something in it failed.
func (r *runner) guardCollection() {
	if v := recover(); v != nil {
		r.send(panicEvent{value: v, where: collectionJob})
		r.send(collectDoneEvent{code: 2, err: fmt.Errorf("internal error in the collection: %v", v)})
	}
}

// readKeys is the keyboard producer. It stops when ReadKey fails — and after
// the wizard has quit, nothing makes that happen: Close restores the terminal
// but does not close t.in, so this goroutine stays parked inside Read until the
// process exits and takes it with it.
//
// That is deliberate rather than overlooked. tui.Run returns to main, which
// exits within microseconds, so the goroutine outlives the wizard by nothing
// worth measuring; and a second shutdown mechanism would not release it anyway,
// since it is inside a syscall and not waiting on a channel. What matters is
// that it cannot block anyone: its only send goes through r.send, which selects
// on done.
func (r *runner) readKeys(t *screen.Terminal) {
	defer r.guard("the keyboard reader")
	for {
		k, err := t.ReadKey()
		if err != nil {
			return
		}
		r.send(pressEvent{key: k, opts: r.opts})
	}
}

// pressEvent is a keystroke as the wizard consumes it: State.Key for all but
// one of them, and the one exception is [g].
//
// [g] writes a file, and Key is a pure function of a keystroke — so the
// decision to write lives here, as state.go says it must. It runs on the loop's
// goroutine rather than in a goroutine of its own for a reason that is easy to
// get backwards: the operator must be told the path or the failure on the very
// next frame, and a grant script is a few kilobytes to a local directory. A
// keystroke that answered three frames later would be a keystroke pressed
// twice, and the second file would be written beside the first.
type pressEvent struct {
	key  screen.Key
	opts collect.Options
}

func (e pressEvent) apply(s State) State {
	if e.key.Rune == 'g' && s.Step == StepVerification {
		return s.writeGrantScript(e.opts.Config.OutputDir, e.opts.Version, time.Now())
	}
	return s.Key(e.key)
}

// connectedEvent is step 1 succeeding. It carries nothing: the address proved
// to answer is already on the state, and the same-day collision cannot be
// answered here — the name the run will use is the one the SERVER gives
// itself, which only the probe brings back.
type connectedEvent struct{}

func (e connectedEvent) apply(s State) State {
	// A result that belongs to an attempt the operator has already cancelled is
	// dropped. Without this test, a connection completing just after ctrl-c
	// would drag screen 1 into the probe the operator declined.
	if s.Step != StepConnecting {
		return s
	}
	s.Step = StepVerifying
	return s
}

// connectFailedEvent is the refusal that does not end the session.
type connectFailedEvent struct{ err error }

func (e connectFailedEvent) apply(s State) State {
	if s.Step != StepConnecting {
		return s
	}
	return s.connectFailed(e.err)
}

// verifiedEvent is the probe coming back, in either of its two shapes: a
// result, or a result that says it could not look.
type verifiedEvent struct {
	result collect.VerifyResult
	err    error
	// collision is what an earlier run of the same day already occupies under
	// the name THIS run will use, or "" when the way is clear. It rides on this
	// event because the probe is the first moment that name is known, and it
	// must be on the state before screen 3 draws: asking on entry to that
	// screen would leave a window in which [enter] starts a run whose prompt
	// had not appeared yet.
	collision string
}

func (e verifiedEvent) apply(s State) State {
	if s.Step != StepVerifying {
		return s
	}
	v := e.result
	// A connection that failed sends the operator back to screen 1, because the
	// address or the password is what has to change. A PROBE that failed does
	// not: it can fail on a denied permission over a perfectly good connection,
	// and sending the operator back to correct a server name that was right
	// would be a lie about where the problem is.
	if v.OpenErr != nil {
		return s.connectFailed(v.OpenErr)
	}
	if v.ConnErr != nil {
		return s.connectFailed(v.ConnErr)
	}
	// Everything else is shown on screen 2, which already draws "not checked"
	// wherever Probed is false. The fatal error is folded into the server block
	// so that the reason is on the screen rather than only in an exit code — an
	// unreadable corpus otherwise produces a screen of blanks explaining
	// nothing.
	if e.err != nil && v.ServerErr == nil {
		v.ServerErr = e.err
	}
	s.Verify = v
	s.Collision = e.collision
	s.Step = StepVerification
	return s
}

// collectDoneEvent is collect.Run returning, however it returned.
type collectDoneEvent struct {
	code  int
	err   error
	total time.Duration
	// ctxCancelled says the wizard's context was cancelled, which decides the
	// EXIT CODE and nothing else. What the final screen says about the archive
	// comes from finishedEvent, which carries the run's own verdict: the two
	// answers differ when ctrl-c lands after the last unit, and only the run
	// knows whether anything was actually cut short.
	ctxCancelled bool
	zipPath      string
	zipBytes     int64
}

func (e collectDoneEvent) apply(s State) State {
	s.Step = StepDone
	s.Stopping = false
	s.Total = e.total
	s.ZipPath, s.ZipBytes = e.zipPath, e.zipBytes
	if e.err != nil {
		// Counted as well as written down. This is the fatal error collect.Run
		// came back with — a lost connection, a reconnect that failed — and it
		// belongs to no unit, so nothing else has counted it: a summary reading
		// "0 errors" under the sentence naming the failure would contradict it.
		s.ErrorCount++
		s.Notes = note(s.Notes, e.err.Error())
	}
	return s
}

// exitStatus is 0 for a run the operator stopped. A cancellation is not a
// failure — the archive is written, marked partial, and worth sending — and a
// script wrapping the wizard must not be told the collection broke because
// somebody decided they had waited long enough.
func (e collectDoneEvent) exitStatus() int {
	if e.ctxCancelled {
		return 0
	}
	return e.code
}

// watchSignals keeps SIGINT and SIGTERM handled, for signals from OUTSIDE this
// terminal only: a kill from another window, a session closing. Raw mode has
// already turned the operator's own ctrl-c into the byte 0x03, which arrives
// through readKeys and never as a signal.
//
// A signal is folded into exactly the keystroke it corresponds to, so there is
// one behaviour to reason about rather than two: on an early screen it quits,
// and during the collection it asks for a stop and lets collect.Run finish
// writing its manifest and its archive.
func (r *runner) watchSignals() {
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		defer r.guard("the signal handler")
		defer signal.Stop(sig)
		for {
			select {
			case <-r.done:
				return
			case <-sig:
				r.send(pressEvent{key: screen.Key{Named: screen.KeyCtrlC}, opts: r.opts})
			}
		}
	}()
}

// stateChanged is the hook loop calls after every event. It is the only
// place that starts or stops anything, which is what keeps the invariant the
// spec insists on: a ticker is started on entry to every waiting step and
// stopped on exit from it, whatever the exit — success, cancellation, error or
// quit — so two are never in flight.
func (r *runner) stateChanged(prev, next State) {
	if next.Step != prev.Step {
		if waiting(prev.Step) {
			r.stopTicker()
			// Leaving a wait abandons the work it started. On the ctrl-c path
			// that is the whole point; on the ordinary path the job has already
			// returned and cancelling its context costs nothing.
			r.stopWork()
		}
		// The step the wizard has just entered, for the timeline. The three
		// below are the ones with a server at the end of them, and a screen
		// that has been spinning for a minute says which of the three it is
		// spinning on only here.
		r.opts.Debugf("wizard step %v → %v", prev.Step, next.Step)
		switch next.Step {
		case StepConnecting:
			r.startTicker()
			r.startWork("the connection", func(ctx context.Context) { r.connect(ctx, next) })
		case StepVerifying:
			r.startTicker()
			r.startWork("the probe", func(ctx context.Context) { r.verify(ctx, next) })
		case StepCollecting:
			r.startTicker()
			r.startWork(collectionJob, func(ctx context.Context) { r.collect(ctx, next) })
		}
	}
	// Ctrl-C during the collection does not change the step — the run is still
	// writing, and saying it is over before it is would end with a partial
	// archive presented as a finished one — so the cancellation has to be
	// noticed here rather than in the switch above.
	if next.Stopping && !prev.Stopping {
		r.stopWork()
	}
}

// waiting reports the three steps that have something running behind them.
func waiting(s Step) bool {
	return s == StepConnecting || s == StepVerifying || s == StepCollecting
}

func (r *runner) startTicker() {
	r.stopTicker()
	ctx, cancel := context.WithCancel(context.Background())
	r.stopTick = cancel
	start := time.Now()
	go func() {
		defer r.guard("the activity indicator")
		tk := time.NewTicker(tickInterval)
		defer tk.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tk.C:
				// The elapsed time is measured from here rather than counted in
				// ticks: a screen accumulating its own interval would drift
				// away from the clock exactly on the runs long enough for the
				// number to matter.
				select {
				case r.events <- tickEvent{elapsed: time.Since(start)}:
				case <-ctx.Done():
					return
				case <-r.done:
					return
				}
			}
		}
	}()
}

func (r *runner) stopTicker() {
	if r.stopTick != nil {
		r.stopTick()
		r.stopTick = nil
	}
}

// startWork runs one step's slow half in its own goroutine, under a context the
// wizard can cancel. The name is carried through to the panic event: three
// goroutines can die here and a bug report that does not say which one is a
// bug report nobody can act on.
func (r *runner) startWork(what string, f func(context.Context)) {
	ctx, cancel := context.WithCancel(context.Background())
	r.stopJob = cancel
	go func() {
		defer r.guard(what)
		defer cancel()
		f(ctx)
	}()
}

func (r *runner) stopWork() {
	if r.stopJob != nil {
		r.stopJob()
		r.stopJob = nil
	}
}

// connect is step 1's slow half: it proves the address, the credentials and the
// encryption settings before the probe spends minutes on them.
//
// It opens a connection and gives it straight back. Verify opens its own — the
// pool allows exactly one, so holding this one would make the probe wait for a
// connection only this function could return — and the cost of the second open
// buys the wizard the one refusal that does not end the session: a wrong
// address or a wrong password lands the operator back on screen 1 with the
// server's own words, instead of on a verification screen reporting eight
// capabilities as unknown.
func (r *runner) connect(ctx context.Context, s State) {
	o := applyState(s, r.opts)
	db, err := collect.Open(o.Config)
	if err != nil {
		r.send(connectFailedEvent{err: err})
		return
	}
	defer db.Close()
	cctx, cancel := context.WithTimeout(ctx, o.Config.ConnectTimeout)
	defer cancel()
	conn, err := db.Conn(cctx)
	if err != nil {
		if ctx.Err() != nil {
			// The operator cancelled; the refusal is ours, not the server's,
			// and the state has already moved back to screen 1.
			return
		}
		r.send(connectFailedEvent{err: err})
		return
	}
	_ = conn.Close()
	if ctx.Err() != nil {
		return
	}
	r.send(connectedEvent{})
}

func (r *runner) verify(ctx context.Context, s State) {
	o := applyState(s, r.opts)
	// The two halves in order: the local one cannot fail on the network, and
	// the server one is where the minutes go. The wizard has no listing to
	// print between them — its spinner is already saying the same thing — but
	// it must still stop on a corpus it could not read rather than probe an
	// instance for a plan it cannot build.
	v, err := collect.VerifyLocal(o)
	if err == nil {
		err = collect.VerifyServer(ctx, o, &v)
	}
	if ctx.Err() != nil {
		return
	}
	// The collision is answered here and not at step 1: the folder collect.Run
	// destroys is named after SERVERPROPERTY('ServerName'), which the probe has
	// just brought back, and the address the operator typed is not it.
	s.Verify = v
	r.send(verifiedEvent{result: v, err: err, collision: collisionFor(s, o)})
}

func (r *runner) collect(ctx context.Context, s State) {
	// Before anything else, and inside this function rather than in startWork:
	// this recover runs first and consumes the panic, so the generic guard
	// deferred one frame up finds nothing left to convert. See guardCollection
	// for why this goroutine needs two events where the others need one.
	defer r.guardCollection()
	o := applyState(s, r.opts)
	o.Observer = observer{ch: r.events}
	// The wizard is painting the terminal, so Run must put nothing on stdout.
	o.OwnsScreen = true
	// The archive's name is derived before the run rather than read from it,
	// because Run reports it on stdout only when OwnsScreen is unset — under
	// the wizard that write is suppressed, since it would land in the middle of
	// a frame. RunFolderFor is deterministic given the same output directory,
	// server name, clock and --keep, and runFolderFor passes it the same four
	// values Run itself will use a microsecond later — the probed name among
	// them, which is the whole point.
	folder := runFolderFor(s, o)
	start := time.Now()
	code, err := collect.Run(ctx, o)
	e := collectDoneEvent{
		code:         code,
		err:          err,
		total:        time.Since(start),
		ctxCancelled: ctx.Err() != nil,
	}
	// The size comes from the file itself, not from a counter: what the
	// operator is about to attach is the archive on disk, and a total of the
	// bytes written before compression would be a different, larger number
	// presented as the same thing.
	zip := folder + ".zip"
	if abs, aerr := filepath.Abs(zip); aerr == nil {
		zip = abs
	}
	if fi, serr := os.Stat(zip); serr == nil {
		e.zipPath, e.zipBytes = zip, fi.Size()
	}
	r.send(e)
}

// applyState overlays what the operator typed and ticked onto the resolved
// configuration, and returns the Options the next job runs on.
//
// It copies the Config rather than writing through the pointer: the same
// Options value is reused for every attempt — a re-probe, a second connection
// after a corrected address — and a mutation would make each attempt inherit
// the last one's edits, including a password typed against a server the
// operator has since replaced.
//
// It is NOT buildOptions, which resolves flags, .env and the environment into
// the Options this one refines. Two functions, two jobs.
func applyState(s State, o collect.Options) collect.Options {
	cfg := *o.Config
	cfg.Server = s.Server
	// Only when something was typed. The field starts empty even when .env
	// holds a password — the wizard displays the effect of .env, it does not
	// read a secret back onto a screen — so an empty field means "use what was
	// resolved", not "connect with no password".
	if s.Password != "" {
		cfg.Password = s.Password
	}
	o.Config = &cfg
	flags := make(map[string]bool, len(s.Flags))
	for k, v := range s.Flags {
		flags[k] = v
	}
	o.Flags = flags
	o.Keep = s.Keep
	return o
}

// initialState is screen 1 as the resolved configuration leaves it.
func initialState(o collect.Options, ascii bool) State {
	cfg := o.Config
	flags := make(map[string]bool, len(o.Flags))
	for k, v := range o.Flags {
		flags[k] = v
	}
	return State{
		Step:       StepConnection,
		Version:    o.Version,
		Build:      o.Commit,
		Server:     cfg.Server,
		Catalog:    cfg.Database,
		User:       cfg.User,
		Integrated: cfg.Integrated,
		Encrypt:    cfg.Encrypt,
		TrustCert:  cfg.TrustCert,
		Source:     "Read from .env and the environment.",
		OutputDir:  cfg.OutputDir,
		Field:      fieldServer,
		Flags:      flags,
		Keep:       o.Keep,
		ASCII:      ascii,

		QueryStoreWindow: queryStoreWindow(cfg),
	}
}

// queryStoreWindow spells out the window screen 3 shows above the two Query
// Store opt-ins. The flags there decide what those collectors export and the
// window is what bounds it, so ticking one without knowing the other is
// choosing half a decision.
//
// The bounds are in the SERVER's local time, and the sentence says so: that is
// the one detail that turns a window into the wrong hour of the wrong day.
func queryStoreWindow(cfg *collect.Config) string {
	switch {
	case cfg.QueryStoreFrom != "" && cfg.QueryStoreTo != "":
		return cfg.QueryStoreFrom + " to " + cfg.QueryStoreTo + ", server local time"
	case cfg.QueryStoreFrom != "":
		return "from " + cfg.QueryStoreFrom + " to the moment of collection, server local time"
	case cfg.QueryStoreTo != "":
		return "the seven days ending " + cfg.QueryStoreTo + ", server local time"
	}
	return fmt.Sprintf("last %d days", cfg.QueryStoreDays)
}

// syncBuffer is the progress buffer, guarded. collect.Run writes to it from its
// own goroutine, and the exit path reads it from the main one; on the path
// where the wizard ends while a collection is still unwinding, those two
// overlap. A bytes.Buffer is not safe for that, and the failure would be a
// corrupted or truncated run log on exactly the runs that produced one.
//
// io.Discard is forbidden on this path and so is a buffer nobody empties: when
// lastResort fires, these bytes are the only surviving copy of the manifest.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// snapshot hands flushProgress a *bytes.Buffer of its own, so the file is
// written from a copy nothing can still be appending to.
func (b *syncBuffer) snapshot() *bytes.Buffer {
	b.mu.Lock()
	defer b.mu.Unlock()
	return bytes.NewBuffer(append([]byte(nil), b.buf.Bytes()...))
}
