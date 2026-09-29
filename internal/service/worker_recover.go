package service

import "log"

// recoverWorkerPanic must be deferred at the top of every background worker
// tick (scheduler, lease heartbeat, regression poller, webhook retry). A
// panic in one iteration — a nil dereference decoding a webhook response, a
// malformed stored plan — must not take down the whole process: every org's
// investigations depend on this server staying up, not just the job that
// panicked. The next tick picks the work back up (durable leases / advisory
// locks make each iteration safe to retry).
func recoverWorkerPanic(component string) {
	if r := recover(); r != nil {
		log.Printf("%s: recovered from panic: %v", component, r)
	}
}
