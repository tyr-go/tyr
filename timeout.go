package tyr

import (
	"context"
	"fmt"
	"time"
)

// timeoutKey is the key of Timeout; register reads it into
// Operation.timeout.
var timeoutKey = NewMetaKey[time.Duration]("tyr.timeout")

// Timeout limits a call of the operation to d: after d, the context of its
// interceptors and handler is done, with [context.DeadlineExceeded], and a
// handler that returns the error of that context fails the call with
// [KindDeadlineExceeded]. The clock starts when the call does: each call of
// a JSON-RPC batch has its own, and the time a call waits for its turn
// doesn't count. An earlier deadline of the caller's context stays.
//
// The timeout is cooperative, and the handler must listen to its context:
// Timeout doesn't cut a call short. A call that ignores it runs on, and its
// result, a success or an error, is what the client gets, since the work
// is done by then; the API logs a warning for it, "tyr: call outlived its
// timeout", with the timeout and the time the call took. Keep the timeout
// below the time the transport has to write a response, such as the
// WriteTimeout of an http.Server.
//
// Set it on a group to limit several operations; when several options set
// it, the last one applied wins, as with a [MetaKey]. Timeout also declares
// KindDeadlineExceeded for the documentation of the operation, as
// [Errors] does. It panics if d isn't positive.
func Timeout(d time.Duration) OpOption {
	if d <= 0 {
		panic(fmt.Sprintf("tyr: Timeout(%v): want a positive duration", d))
	}
	declare := Errors(KindDeadlineExceeded)
	return func(op *Operation) {
		timeoutKey.Option(d)(op)
		declare(op)
	}
}

// runWithin runs a call within the operation's timeout, as described at
// Timeout, and warns about a call that took longer, unless it failed with
// its deadline or was canceled.
func (op *Operation) runWithin(ctx context.Context, decode func(dst any) error) (any, error) {
	start := time.Now()
	within, cancel := context.WithTimeout(ctx, op.timeout)
	defer cancel()
	res, err := op.run(within, decode)
	if took := time.Since(start); took > op.timeout && !outOfTime(err) {
		op.api.Logger().WarnContext(ctx, "tyr: call outlived its timeout", "timeout", op.timeout, "took", took)
	}
	return res, err
}

// outOfTime reports whether err, nil or an *Error, is the failure of a call
// that ran out of time or was canceled.
func outOfTime(err error) bool {
	e, ok := err.(*Error)
	return ok && e != nil && (e.Kind == KindDeadlineExceeded || e.Kind == KindCanceled)
}
