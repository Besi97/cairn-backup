package lifecycle

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestConcurrentDeliveryAndIdentity(t *testing.T) {
	var events []Event
	ctx := WithObserver(t.Context(), func(event Event) error {
		events = append(events, event)
		return nil
	})
	due := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	first := New(WithSchedule(ctx, due), Event{Workflow: "nightly"})
	second := New(ctx, Event{Operation: "backup", Preview: true})
	var group sync.WaitGroup
	for range 20 {
		group.Go(func() { first.Emit(Event{Kind: TaskStarted}) })
		group.Go(func() { second.Emit(Event{Kind: TaskFinished, Outcome: Success}) })
	}
	group.Wait()
	sequence := map[string]uint64{}
	for _, event := range events {
		if event.RunID == "" || event.Time.IsZero() || event.Sequence != sequence[event.RunID]+1 {
			t.Fatalf("invalid identity/order: %+v", event)
		}
		sequence[event.RunID]++
		if event.Workflow == "nightly" {
			if event.Source != Scheduled || !event.ScheduledAt.Equal(due) {
				t.Fatalf("missing schedule: %+v", event)
			}
		} else if event.Source != Manual || !event.ScheduledAt.IsZero() || !event.Preview || event.Operation != "backup" {
			t.Fatalf("missing manual attribution: %+v", event)
		}
	}
	if len(sequence) != 2 || len(events) != 40 {
		t.Fatalf("ids=%v events=%d", sequence, len(events))
	}
}

func TestDeliveryErrorsAreRedactedAndDoNotDisableDelivery(t *testing.T) {
	calls := 0
	ctx := WithObserver(t.Context(), func(Event) error {
		calls++
		return errors.New("secret from observer")
	})
	run := New(ctx, Event{})
	run.Emit(Event{Kind: RunStarted})
	run.Emit(Event{Kind: RunFinished, Outcome: Success})
	if calls != 2 || Err(ctx) != ErrDelivery {
		t.Fatalf("calls=%d error=%v", calls, Err(ctx))
	}
	if Err(t.Context()) != nil {
		t.Fatal("unobserved context has a delivery error")
	}
}
