package service

import "testing"

// A panic during a background worker tick must not propagate and kill the
// process — every org's investigations depend on the server staying up, not
// just the job that panicked. If recoverWorkerPanic did not recover, this
// test itself would crash instead of reaching the assertion.
func TestRecoverWorkerPanic_SwallowsPanic(t *testing.T) {
	ran := false
	func() {
		defer recoverWorkerPanic("test_component")
		panic("boom")
	}()
	ran = true
	if !ran {
		t.Fatal("unreachable: panic should have been recovered")
	}
}

// Pins the same defer-inside-a-tick pattern used by the schedule heartbeat and
// webhook retry loops (an anonymous func wrapping the tick body), where the
// call site cannot be unit-tested directly because it is a private closure
// inside a long-running goroutine.
func TestRecoverWorkerPanic_TickClosurePatternSurvives(t *testing.T) {
	tick := func() {
		func() {
			defer recoverWorkerPanic("tick_pattern")
			panic("simulated failure mid-tick")
		}()
	}
	tick() // must return normally, not crash the test
}
