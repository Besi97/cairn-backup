package lifecycle

import (
	"errors"
	"fmt"
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

func TestAdditionalObserversRetainDeliveryAndFailures(t *testing.T) {
	for _, failing := range []int{-1, 0, 1, 2} {
		t.Run(fmt.Sprintf("failing=%d", failing), func(t *testing.T) {
			var calls [3][]Event
			observe := func(index int) Observer {
				return func(event Event) error {
					calls[index] = append(calls[index], event)
					if index == failing {
						return errors.New("private observer error")
					}
					return nil
				}
			}
			parent := WithObserver(t.Context(), observe(0))
			ctx := WithAdditionalObserver(parent, observe(1))
			ctx = WithAdditionalObserver(ctx, observe(2))
			run := New(ctx, Event{Workflow: "daily"})
			run.Emit(Event{Kind: RunStarted})
			run.Emit(Event{Kind: RunFinished, Outcome: Success})
			for index, events := range calls {
				if len(events) != 2 || events[1].Outcome != Success {
					t.Fatalf("observer %d: %+v", index, events)
				}
				if events[0] != calls[0][0] || events[1] != calls[0][1] {
					t.Fatal("observers received different events")
				}
			}
			if (Err(ctx) == ErrDelivery) != (failing >= 0) {
				t.Fatalf("delivery error: %v", Err(ctx))
			}
			if (Err(parent) == ErrDelivery) != (failing == 0) {
				t.Fatalf("parent error: %v", Err(parent))
			}
		})
	}
}

func TestAdditionalObserverSeesExistingAndLaterParentFailures(t *testing.T) {
	parent := WithObserver(t.Context(), func(Event) error { return errors.New("private") })
	New(parent, Event{}).Emit(Event{Kind: RunStarted})
	ctx := WithAdditionalObserver(parent, nil)
	if Err(ctx) != ErrDelivery {
		t.Fatal("existing failure lost")
	}
	parent = WithObserver(t.Context(), func(Event) error { return errors.New("private") })
	ctx = WithAdditionalObserver(parent, nil)
	New(parent, Event{}).Emit(Event{Kind: RunFinished, Outcome: Failure})
	if Err(ctx) != ErrDelivery {
		t.Fatal("later parent failure lost")
	}
}

func TestAdditionalObserverWithoutParentAndReplacement(t *testing.T) {
	calls := 0
	ctx := WithAdditionalObserver(t.Context(), func(Event) error {
		calls++
		return nil
	})
	New(ctx, Event{}).Emit(Event{Kind: RunStarted})
	if calls != 1 || Err(ctx) != nil {
		t.Fatalf("calls=%d error=%v", calls, Err(ctx))
	}
	ctx = WithObserver(ctx, nil)
	New(ctx, Event{}).Emit(Event{Kind: RunFinished})
	if calls != 1 {
		t.Fatal("WithObserver no longer replaces observers")
	}
}

func TestAdditionalObserversSerializeSharedParent(t *testing.T) {
	var parentEvents, firstEvents, secondEvents []Event
	parent := WithObserver(t.Context(), func(event Event) error {
		parentEvents = append(parentEvents, event)
		return errors.New("private")
	})
	first := WithAdditionalObserver(parent, func(event Event) error {
		firstEvents = append(firstEvents, event)
		return nil
	})
	second := WithAdditionalObserver(parent, func(event Event) error {
		secondEvents = append(secondEvents, event)
		return nil
	})
	var group sync.WaitGroup
	for range 20 {
		group.Go(func() { New(first, Event{}).Emit(Event{Kind: RunStarted}) })
		group.Go(func() { New(second, Event{}).Emit(Event{Kind: RunStarted}) })
		group.Go(func() { New(parent, Event{}).Emit(Event{Kind: RunStarted}) })
	}
	group.Wait()
	if len(parentEvents) != 60 || len(firstEvents) != 20 || len(secondEvents) != 20 {
		t.Fatalf("deliveries: %d/%d/%d", len(parentEvents), len(firstEvents), len(secondEvents))
	}
	if Err(parent) != ErrDelivery || Err(first) != ErrDelivery || Err(second) != ErrDelivery {
		t.Fatal("delivery failure lost")
	}
}
