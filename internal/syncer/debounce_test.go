package syncer

import (
	"context"
	"testing"
	"time"
)

func TestDebouncerCoalescesBursts(t *testing.T) {
	t.Parallel()

	d := &Debouncer[string]{Interval: 50 * time.Millisecond, MaxWait: time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	in := make(chan string)
	out := make(chan []string, 4)
	go d.Run(ctx, in, out)

	// A burst of five events, each well inside the quiet period.
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		in <- name
		time.Sleep(5 * time.Millisecond)
	}

	batch := receiveBatch(t, out, 2*time.Second)
	if len(batch) != 5 {
		t.Fatalf("batch = %v, want all five events coalesced", batch)
	}

	select {
	case extra := <-out:
		t.Fatalf("got a second batch %v, want exactly one", extra)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestDebouncerSeparatesDistinctBursts(t *testing.T) {
	t.Parallel()

	d := &Debouncer[string]{Interval: 40 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	in := make(chan string)
	out := make(chan []string, 4)
	go d.Run(ctx, in, out)

	in <- "first"
	if batch := receiveBatch(t, out, time.Second); len(batch) != 1 || batch[0] != "first" {
		t.Fatalf("first batch = %v, want [first]", batch)
	}

	in <- "second"
	if batch := receiveBatch(t, out, time.Second); len(batch) != 1 || batch[0] != "second" {
		t.Fatalf("second batch = %v, want [second]", batch)
	}
}

func TestDebouncerMaxWaitCapsContinuousStream(t *testing.T) {
	t.Parallel()

	d := &Debouncer[int]{Interval: 200 * time.Millisecond, MaxWait: 120 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	in := make(chan int)
	out := make(chan []int, 4)
	go d.Run(ctx, in, out)

	start := time.Now()
	stopFeeding := make(chan struct{})
	defer close(stopFeeding)
	go func() {
		for i := 0; ; i++ {
			select {
			case <-stopFeeding:
				return
			case in <- i:
			case <-ctx.Done():
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()

	batch := receiveBatch(t, out, 2*time.Second)
	elapsed := time.Since(start)

	if len(batch) == 0 {
		t.Fatal("batch is empty")
	}
	// Without MaxWait the constant stream would keep resetting the timer and
	// nothing would ever be emitted.
	if elapsed > 500*time.Millisecond {
		t.Errorf("batch emitted after %v, want MaxWait (~120ms) to cap the burst", elapsed)
	}
}

func TestDebouncerFlushesWhenInputCloses(t *testing.T) {
	t.Parallel()

	d := &Debouncer[string]{Interval: time.Hour}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	in := make(chan string, 1)
	out := make(chan []string, 1)
	go d.Run(ctx, in, out)

	in <- "pending"
	close(in)

	if batch := receiveBatch(t, out, time.Second); len(batch) != 1 {
		t.Fatalf("batch = %v, want the pending event flushed on close", batch)
	}
}

func TestDebouncerStopsOnContextCancel(t *testing.T) {
	t.Parallel()

	d := &Debouncer[string]{Interval: 10 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())

	in := make(chan string)
	out := make(chan []string, 1)
	done := make(chan struct{})
	go func() {
		d.Run(ctx, in, out)
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run() did not return after the context was cancelled")
	}
}

func receiveBatch[T any](t *testing.T, out <-chan []T, timeout time.Duration) []T {
	t.Helper()
	select {
	case batch := <-out:
		return batch
	case <-time.After(timeout):
		t.Fatal("timed out waiting for a batch")
		return nil
	}
}
