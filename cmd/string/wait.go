package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// agentWaitLimit is how long a wait lasts under a coding agent unless
// --timeout says otherwise: under the two minutes some agents give a command,
// so string stops first and says how to resume.
const agentWaitLimit = 90 * time.Second

// waitHeartbeat is how often a wait prints a plain line where no person
// watches a redrawn one. It shows progress in a CI log, and resets the
// silence timers some agents keep on a command's output.
const waitHeartbeat = 15 * time.Second

// waitLimit is how long a wait may last: --timeout when it was given, 90s
// under an agent, and otherwise until the job ends or the code expires.
func (rt runtime) waitLimit() time.Duration {
	switch {
	case rt.timeout != nil:
		return *rt.timeout
	case rt.exec.agent != "":
		return agentWaitLimit
	}
	return 0
}

// waiter keeps a wait within its limit and never silent.
type waiter struct {
	rt       runtime
	limit    time.Duration // zero for none
	started  time.Time
	lastBeat time.Time
	// redrawn is set once a progress line is on the terminal, so the wait
	// ends that line before anything else is printed.
	redrawn bool
	// begun is set once the wait has said what it waits for.
	begun bool
}

// newWaiter starts a wait whose limit counts from started.
func newWaiter(rt runtime, started time.Time) *waiter {
	return &waiter{rt: rt, limit: rt.waitLimit(), started: started, lastBeat: started}
}

// left is how much of the wait's limit is left, or false when it has none.
func (w *waiter) left() (time.Duration, bool) {
	if w.limit == 0 {
		return 0, false
	}
	return w.limit - w.rt.clock().Sub(w.started), true
}

// next is the runtime for the wait's next request, which may take no longer
// than the wait has left, and false once nothing is left.
func (w *waiter) next() (runtime, bool) {
	rt := w.rt
	left, limited := w.left()
	switch {
	case !limited:
		return rt, true
	case left <= 0:
		return rt, false
	case rt.limit() == 0 || left < rt.limit():
		rt.timeout = &left
	}
	return rt, true
}

// over reports whether the wait's limit has passed.
func (w *waiter) over() bool {
	left, limited := w.left()
	return limited && left <= 0
}

// pause waits d before the next request, or only until the wait's limit when
// that comes first.
func (w *waiter) pause(d time.Duration) {
	if left, limited := w.left(); limited && left < d {
		d = max(left, 0)
	}
	w.rt.pause(d)
}

// begin prints line, which says what the wait is for and the command that
// resumes it, once and only where no person watches stderr at a terminal.
func (w *waiter) begin(line string) {
	if w.begun {
		return
	}
	w.begun = true
	if !w.rt.exec.redraws() && !w.rt.quiet {
		fmt.Fprintln(w.rt.stderr, line)
	}
}

// progress shows how the wait stands: redrawn in place for a person who sees
// stderr at a terminal, unless redrawn is empty, and elsewhere as a plain
// line every waitHeartbeat.
func (w *waiter) progress(redrawn, plain string) {
	if w.rt.quiet {
		return
	}
	if w.rt.exec.redraws() {
		if redrawn != "" {
			fmt.Fprintf(w.rt.stderr, "\r\x1b[K%s", redrawn)
			w.redrawn = true
		}
		return
	}
	now := w.rt.clock()
	if now.Sub(w.lastBeat) < waitHeartbeat {
		return
	}
	w.lastBeat = now
	fmt.Fprintf(w.rt.stderr, "waiting: %s · %s\n", plain, elapsed(now.Sub(w.started)))
}

// end finishes a redrawn line, so whatever follows starts on its own line.
func (w *waiter) end() {
	if w.redrawn {
		fmt.Fprintln(w.rt.stderr)
		w.redrawn = false
	}
}

// stopped is the failure for a wait that reached its limit: what is still
// going on without string, and the command that picks the wait up again.
func (w *waiter) stopped(carriesOn, resume string) *failure {
	return &failure{
		exit:     exitTimeout,
		code:     "wait_timeout",
		message:  fmt.Sprintf("Stopped waiting after %s; %s.", shortDuration(w.limit), carriesOn),
		nextStep: "run `" + resume + "`",
	}
}

// stoppedBefore is stopped for a wait on a code or a link, what, that works
// until expires: it says how long that has left, or is expired once it has
// less than a second.
func (w *waiter) stoppedBefore(expires time.Time, what, resume string, expired error) error {
	left := expires.Sub(w.rt.clock())
	if left < time.Second {
		return expired
	}
	return w.stopped(what+" expires in "+elapsed(left), resume)
}

// elapsed writes how long a wait has lasted, as 45s or 1m15s.
func elapsed(d time.Duration) string {
	d = d.Round(time.Second)
	if d < time.Minute {
		return strconv.FormatInt(int64(d/time.Second), 10) + "s"
	}
	text := strconv.FormatInt(int64(d/time.Minute), 10) + "m"
	if seconds := d % time.Minute; seconds != 0 {
		text += strconv.FormatInt(int64(seconds/time.Second), 10) + "s"
	}
	return text
}

// count writes n of noun, as 1 page or 1,050 pages.
func count(n int64, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return thousands(n) + " " + noun + "s"
}

// thousands writes n with commas between groups of three digits, as 1,050.
func thousands(n int64) string {
	digits := strconv.FormatInt(n, 10)
	sign := ""
	if n < 0 {
		sign, digits = "-", digits[1:]
	}
	var out strings.Builder
	for i, digit := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			out.WriteByte(',')
		}
		out.WriteRune(digit)
	}
	return sign + out.String()
}
