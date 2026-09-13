package libxfat

import "context"

// cancellationCheckInterval is how many units of work a cancellable operation
// performs between checks of its context.
//
// A check is cheap but not free, and the operations that take one - a tree walk,
// a recovery sweep - do very little per unit, so checking every unit would cost
// more than the work being paced. The counter that drives it spans the whole
// operation rather than restarting per directory or per cluster, so a walk over a
// million small directories stays interruptible; and it is tested before it
// advances, so a context that is already cancelled is caught on the first unit
// rather than the thousandth.
//
// This matches the sibling FAT library's pacing deliberately: a consumer driving
// both should not find that one of them notices a cancellation and the other does
// not.
const cancellationCheckInterval = 1024

// scanProgress paces cancellation checks across one whole operation.
//
// It is a single counter shared by every phase of that operation, which is what
// keeps the pacing rule from drifting between phases: a walk that also recovers
// deleted entries checks its tree traversal and its cluster sweep against the
// same counter rather than two that each start again at zero.
type scanProgress struct {
	ctx     context.Context
	counter uint64
}

// check reports the context's error every cancellationCheckInterval calls. The
// counter is tested before it advances, so the first call always checks.
func (p *scanProgress) check() error {
	if p.counter%cancellationCheckInterval == 0 {
		if err := p.ctx.Err(); err != nil {
			return err
		}
	}
	p.counter++
	return nil
}
