// Package syncer contains the event debouncer, the thread-safe state cache
// and the single-threaded reconcile worker.
package syncer

import (
	"context"
	"time"
)

// Debouncer coalesces bursts of events (think `docker compose up`) into a
// single batch. A batch is emitted once no new event arrived for Interval,
// and at the latest MaxWait after the first event of the burst.
type Debouncer[T any] struct {
	// Interval is the quiet period after the last event.
	Interval time.Duration
	// MaxWait caps how long a continuous stream of events can delay a batch.
	// Zero disables the cap.
	MaxWait time.Duration
	// Now is injectable for tests; defaults to time.Now.
	Now func() time.Time
}

func (d *Debouncer[T]) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// Run pumps events from in to out until ctx is cancelled or in is closed.
// It never blocks the producer for longer than one batch handover, so the
// Docker event stream cannot back up while the NPM API is slow.
//
// On shutdown any pending batch is dropped on purpose: the worker performs a
// full reconcile before exiting, which supersedes individual events.
func (d *Debouncer[T]) Run(ctx context.Context, in <-chan T, out chan<- []T) {
	interval := d.Interval
	if interval <= 0 {
		interval = time.Second
	}

	var (
		pending  []T
		timer    *time.Timer
		timerC   <-chan time.Time
		deadline time.Time
	)
	stopTimer := func() {
		if timer == nil {
			return
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer, timerC = nil, nil
	}
	defer stopTimer()

	for {
		select {
		case <-ctx.Done():
			return

		case event, ok := <-in:
			if !ok {
				// Producer is gone: emit what we have and stop.
				if len(pending) > 0 {
					select {
					case out <- pending:
					case <-ctx.Done():
					}
				}
				return
			}

			pending = append(pending, event)
			wait := interval
			now := d.now()
			if timer == nil {
				if d.MaxWait > 0 {
					deadline = now.Add(d.MaxWait)
				} else {
					deadline = time.Time{}
				}
				timer = time.NewTimer(wait)
				timerC = timer.C
				continue
			}
			if !deadline.IsZero() {
				if remaining := deadline.Sub(now); remaining < wait {
					wait = max(remaining, 0)
				}
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(wait)
			timerC = timer.C

		case <-timerC:
			timer, timerC = nil, nil
			batch := pending
			pending = nil
			select {
			case out <- batch:
			case <-ctx.Done():
				return
			}
		}
	}
}
