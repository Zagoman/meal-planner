package events

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestPublishOnlyReachesOwnHousehold(t *testing.T) {
	h := NewHub()
	a, b := uuid.New(), uuid.New()
	chA, cancelA := h.Subscribe(a)
	defer cancelA()
	chB, cancelB := h.Subscribe(b)
	defer cancelB()

	id := uuid.New()
	h.Publish(a, Event{Kind: ItemChanged, ItemID: id})

	select {
	case e := <-chA:
		if e.ItemID != id {
			t.Fatalf("got %v, want %v", e.ItemID, id)
		}
	case <-time.After(time.Second):
		t.Fatal("household A did not get its event")
	}
	select {
	case e := <-chB:
		t.Fatalf("household B received A's event: %+v", e)
	default:
	}
}

func TestCancelIsIdempotentAndCloses(t *testing.T) {
	h := NewHub()
	ch, cancel := h.Subscribe(uuid.New())
	cancel()
	cancel()
	if _, ok := <-ch; ok {
		t.Fatal("channel not closed")
	}
}
