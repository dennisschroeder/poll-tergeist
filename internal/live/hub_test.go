package live

import (
	"testing"
	"time"
)

// TestSubscribeThenInvalidate_Delivers proves the base case: once Subscribe
// has returned, an Invalidate for that poll is observed by the subscriber.
func TestSubscribeThenInvalidate_Delivers(t *testing.T) {
	h := NewHub()
	ch, unsubscribe := h.Subscribe("poll-1")
	defer unsubscribe()

	h.Invalidate("poll-1")

	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("subscriber never received an invalidation sent after Subscribe returned")
	}
}

// TestInvalidateImmediatelyAfterSubscribe demonstrates the property the SSE
// handler's subscribe-before-read ordering depends on (see
// docs/adr/0003-tally-fan-out-and-queue-design.md): a vote that commits the
// instant after Subscribe returns is never missed, because the channel is
// already registered before the caller does anything else.
func TestInvalidateImmediatelyAfterSubscribe(t *testing.T) {
	h := NewHub()
	ch, unsubscribe := h.Subscribe("poll-1")
	defer unsubscribe()

	// Nothing happens between Subscribe and Invalidate here — that's the
	// point. There is no window for a "read tally, subscribe" ordering bug
	// to exist, because subscription is already complete.
	h.Invalidate("poll-1")

	select {
	case <-ch:
	default:
		t.Fatal("invalidation sent right after Subscribe was not pending on the channel")
	}
}

// TestInvalidate_CoalescesWhilePending proves the coalescing contract: many
// invalidations while a subscriber isn't consuming collapse into exactly
// one pending signal, never more — the buffer stays bounded at 1 regardless
// of how many votes land.
func TestInvalidate_CoalescesWhilePending(t *testing.T) {
	h := NewHub()
	ch, unsubscribe := h.Subscribe("poll-1")
	defer unsubscribe()

	const n = 500
	for i := 0; i < n; i++ {
		h.Invalidate("poll-1")
	}

	if got := len(ch); got != 1 {
		t.Fatalf("pending signals = %d, want exactly 1 after %d invalidations", got, n)
	}

	// Consume the one pending signal.
	<-ch
	select {
	case <-ch:
		t.Fatal("received a second signal — coalescing should have left only one")
	default:
	}

	// The subscriber is caught up now; one more invalidation still arrives.
	h.Invalidate("poll-1")
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("invalidation after catching up was not delivered")
	}
}

// TestInvalidate_NeverBlocks proves publication is non-blocking even with
// nobody ever consuming — a slow or stalled subscriber must not stall the
// voter whose request triggered the invalidation.
func TestInvalidate_NeverBlocks(t *testing.T) {
	h := NewHub()
	_, unsubscribe := h.Subscribe("poll-1")
	defer unsubscribe()

	done := make(chan struct{})
	go func() {
		for i := 0; i < 10_000; i++ {
			h.Invalidate("poll-1")
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Invalidate blocked with an unconsumed subscriber")
	}
}

// TestInvalidate_UnrelatedPollNotAffected proves fan-out is scoped per poll.
func TestInvalidate_UnrelatedPollNotAffected(t *testing.T) {
	h := NewHub()
	chA, unsubA := h.Subscribe("poll-A")
	defer unsubA()
	chB, unsubB := h.Subscribe("poll-B")
	defer unsubB()

	h.Invalidate("poll-B")

	select {
	case <-chB:
	default:
		t.Fatal("poll-B subscriber did not receive its invalidation")
	}
	select {
	case <-chA:
		t.Fatal("poll-A subscriber received an invalidation meant for poll-B")
	default:
	}
}

// TestUnsubscribe_ClosesChannel proves the channel is closed and no longer
// tracked once unsubscribe runs, so it can be garbage collected and further
// Invalidate calls for that poll don't panic or leak.
func TestUnsubscribe_ClosesChannel(t *testing.T) {
	h := NewHub()
	ch, unsubscribe := h.Subscribe("poll-1")
	unsubscribe()

	if _, ok := <-ch; ok {
		t.Fatal("channel was not closed by unsubscribe")
	}

	// Must not panic with no subscribers left.
	h.Invalidate("poll-1")
}
