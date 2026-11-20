package main

import (
	"io"
	"os"
	"os/signal"
	"sync"
)

// interruption is what a Ctrl-C would leave behind at this moment: what keeps
// running without string, or what may already have happened, and the command
// that picks it up. Commands note it while they wait or send.
type interruption struct {
	mu   sync.Mutex
	what string
	next string
	// json makes the message the --json error envelope.
	json bool
	// clears says that a person watches stderr at a terminal, where the line
	// that Ctrl-C echoed into, or a redrawn progress line, is cleared first.
	clears bool
}

// note says what a Ctrl-C from now on leaves behind, until the func it
// returns puts back what was said before.
func (i *interruption) note(what, next string) func() {
	if i == nil {
		return func() {}
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	was, wasNext := i.what, i.next
	i.what, i.next = what, next
	return func() {
		i.mu.Lock()
		defer i.mu.Unlock()
		i.what, i.next = was, wasNext
	}
}

// answer sets how the message is written: as the --json error envelope, and
// after clearing the line for a person who watches stderr at a terminal.
func (i *interruption) answer(json, clears bool) {
	if i == nil {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.json, i.clears = json, clears
}

// report prints what a Ctrl-C leaves behind, and nothing when nothing keeps
// running and nothing may have happened.
func (i *interruption) report(stderr io.Writer) {
	i.mu.Lock()
	what, next, json, clears := i.what, i.next, i.json, i.clears
	i.mu.Unlock()
	if what == "" {
		return
	}
	if clears {
		io.WriteString(stderr, "\r\x1b[K")
	}
	said := &failure{exit: exitInterrupted, code: "interrupted", message: "Interrupted; " + what + "."}
	if next != "" {
		said.nextStep = "run `" + next + "`"
	}
	report(stderr, said, json)
}

// stopOnInterrupt ends string on Ctrl-C. The first says what keeps running or
// may already have happened, then string ends as an interrupted program does,
// so its shell sees that. A second ends it at once, even while the first is
// still being said.
func stopOnInterrupt(i *interruption, stderr io.Writer) {
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, os.Interrupt)
	go func() {
		<-signals
		said := make(chan struct{})
		go func() {
			i.report(stderr)
			close(said)
		}()
		select {
		case <-said:
		case <-signals:
		}
		endInterrupted()
	}()
}
