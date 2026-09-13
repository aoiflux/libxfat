package libxfat

import (
	"context"
	"errors"
	"testing"
)

// TestScanProgressChecksOnceAnInterval covers the pacing rule directly, which is
// the only place it can be covered cheaply: a walk would have to report more than
// cancellationCheckInterval entries to reach its second check, and no fixture in
// this repository is that large.
func TestScanProgressChecksOnceAnInterval(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	prog := &scanProgress{ctx: ctx}

	// A live context passes at the boundary and everywhere between. This walks
	// the counter up to one short of the next boundary.
	for i := 0; i < cancellationCheckInterval-1; i++ {
		if err := prog.check(); err != nil {
			t.Fatalf("check %d on a live context = %v, want nil", i, err)
		}
	}

	cancel()

	// Still inside the interval, so the cancellation is not seen yet. This is the
	// whole point of the pacing and the reason a walk over a small tree completes
	// after its context is cancelled.
	if err := prog.check(); err != nil {
		t.Fatalf("check inside the interval after cancelling = %v, want nil", err)
	}

	// The next call lands on the boundary and reports it.
	if err := prog.check(); !errors.Is(err, context.Canceled) {
		t.Fatalf("check at the interval boundary = %v, want context.Canceled", err)
	}
}

// TestScanProgressCancellationIsSticky pins the counter not advancing once a
// cancellation has been reported. An operation that is winding down must not
// report success from its next thousand checks just because it consumed the one
// that failed.
func TestScanProgressCancellationIsSticky(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	prog := &scanProgress{ctx: ctx}
	for i := 0; i < 3; i++ {
		if err := prog.check(); !errors.Is(err, context.Canceled) {
			t.Fatalf("check %d = %v, want context.Canceled every time", i, err)
		}
	}
}

// TestScanProgressLiveContextNeverFires guards against the check reporting an
// error for a context that is fine, which would make every long walk fail.
func TestScanProgressLiveContextNeverFires(t *testing.T) {
	prog := &scanProgress{ctx: context.Background()}
	for i := 0; i < cancellationCheckInterval*3; i++ {
		if err := prog.check(); err != nil {
			t.Fatalf("check %d on a live context = %v, want nil", i, err)
		}
	}
}
