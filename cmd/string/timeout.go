package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"
)

// How long one request to String may take unless --timeout says otherwise.
// Under a coding agent every request gets 90s, under the two minutes some
// agents give a command before they stop it.
const (
	requestLimit            = 120 * time.Second
	integrationRequestLimit = 300 * time.Second
	agentRequestLimit       = 90 * time.Second
)

// bodyIdleLimit is how long an answer may stop arriving before string gives
// up on it. Without it a response that stalls halfway holds the command for
// as long as the request's limit allows, or forever with --timeout 0.
var bodyIdleLimit = 60 * time.Second

var (
	errTookTooLong = errors.New("the request took longer than its limit")
	errWentQuiet   = errors.New("the answer stopped arriving")
)

// parseTimeout reads --timeout: a duration such as 90s, 5m or 1m30s, or a
// number of seconds. 0 means no limit.
func parseTimeout(value string) (time.Duration, error) {
	if seconds, err := strconv.ParseUint(value, 10, 32); err == nil {
		return time.Duration(seconds) * time.Second, nil
	}
	if limit, err := time.ParseDuration(value); err == nil && limit >= 0 {
		return limit, nil
	}
	return 0, fmt.Errorf("--timeout takes a duration such as 90s or 5m, a number of seconds, or 0 for no limit, not %q", value)
}

// limit is how long one request to String may take.
func (rt runtime) limit() time.Duration {
	switch {
	case rt.timeout != nil:
		return *rt.timeout
	case rt.exec.agent != "":
		return agentRequestLimit
	case rt.usualLimit > 0:
		return rt.usualLimit
	}
	return requestLimit
}

// limited sends request and gives up once the whole exchange takes longer
// than rt's limit, or once the answer sends nothing for bodyIdleLimit.
func (rt runtime) limited(request *http.Request) (*http.Response, error) {
	limit := rt.limit()
	ctx, cancel := context.WithCancelCause(request.Context())
	stop := func() { cancel(nil) }
	if limit > 0 {
		var cancelLimit context.CancelFunc
		ctx, cancelLimit = context.WithTimeoutCause(ctx, limit, errTookTooLong)
		stop = func() { cancelLimit(); cancel(nil) }
	}
	timedOut := func() *failure {
		switch context.Cause(ctx) {
		case errTookTooLong:
			return tookTooLong(request.URL.Host, limit, rt.exec.agent != "")
		case errWentQuiet:
			return &failure{exit: exitTimeout, code: "timeout", message: fmt.Sprintf("%s stopped sending its answer: nothing arrived for %s.", request.URL.Host, shortDuration(bodyIdleLimit)), nextStep: "run it again"}
		}
		return nil
	}
	response, err := rt.client.Do(request.WithContext(ctx))
	if err != nil {
		stop()
		if f := timedOut(); f != nil {
			return nil, f
		}
		return nil, networkFailure(rt, request.URL.Host, err)
	}
	body := &watchedBody{ReadCloser: response.Body, stop: stop, timedOut: timedOut}
	body.idle = time.AfterFunc(bodyIdleLimit, func() { cancel(errWentQuiet) })
	response.Body = body
	return response, nil
}

// tookTooLong is the failure for a request that ran past its limit. It offers
// twice the limit, which can be longer than an agent's shell waits for a
// command.
func tookTooLong(host string, limit time.Duration, agent bool) *failure {
	longer := 2 * limit
	next := "run it again with --timeout " + shortDuration(longer)
	if agent && longer > 2*time.Minute {
		next += ", in the background if your shell stops commands after 2 minutes"
	}
	return &failure{exit: exitTimeout, code: "timeout", message: fmt.Sprintf("%s took longer than %s to answer, so string stopped waiting.", host, shortDuration(limit)), nextStep: next}
}

// watchedBody is a response body that limited still watches: each read that
// brings bytes restarts the wait for the next ones.
type watchedBody struct {
	io.ReadCloser
	idle     *time.Timer
	stop     func()
	timedOut func() *failure
}

func (b *watchedBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.idle.Reset(bodyIdleLimit)
	}
	if err != nil && err != io.EOF {
		if f := b.timedOut(); f != nil {
			return n, f
		}
	}
	return n, err
}

func (b *watchedBody) Close() error {
	b.idle.Stop()
	err := b.ReadCloser.Close()
	b.stop()
	return err
}

// shortDuration writes a limit the way --timeout takes it: 90s, 4m, 2m30s.
func shortDuration(d time.Duration) string {
	switch {
	case d%time.Second != 0:
		return d.String()
	case d < 2*time.Minute:
		return strconv.FormatInt(int64(d/time.Second), 10) + "s"
	case d%time.Minute == 0:
		return strconv.FormatInt(int64(d/time.Minute), 10) + "m"
	}
	return d.String()
}
