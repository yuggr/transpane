package monitor

import (
	"testing"

	"github.com/yuggr/transpane/internal/domain"
	"github.com/yuggr/transpane/internal/protection"
)

func job(id string, generation uint64, priority int, segments int) *Job {
	segmentsList := make([]protection.Segment, segments)
	for index := range segmentsList {
		segmentsList[index] = protection.Segment{Text: id}
	}
	return &Job{
		Ref:      domain.TaskRef{Generation: generation, Message: domain.MessageKey{ID: id}},
		Segments: segmentsList,
		Priority: priority,
	}
}

func TestSchedulerPriorityAndSequence(t *testing.T) {
	scheduler := NewScheduler(32)
	scheduler.Submit(job("low", 1, 2, 1))
	scheduler.Submit(job("high", 1, 0, 1))
	scheduler.Submit(job("same", 1, 0, 1))
	first := scheduler.Next()
	if first == nil || first.Ref.Message.ID != "high" {
		t.Fatalf("%+v", first)
	}
	scheduler.Complete(first, false)
	second := scheduler.Next()
	if second == nil || second.Ref.Message.ID != "same" {
		t.Fatalf("%+v", second)
	}
	scheduler.Complete(second, false)
	third := scheduler.Next()
	if third == nil || third.Ref.Message.ID != "low" {
		t.Fatalf("%+v", third)
	}
}

func TestSchedulerDefersBeyondLimit(t *testing.T) {
	scheduler := NewScheduler(3)
	for index := 0; index < 10; index++ {
		scheduler.Submit(job(string(rune('a'+index)), 1, 2, 1))
	}
	if scheduler.Active() != 3 || scheduler.Deferred() != 7 || scheduler.Pending() != 10 {
		t.Fatalf("active=%d deferred=%d pending=%d", scheduler.Active(), scheduler.Deferred(), scheduler.Pending())
	}
	seen := map[string]bool{}
	for {
		j := scheduler.Next()
		if j == nil {
			break
		}
		seen[j.Ref.Message.ID] = true
		scheduler.Complete(j, false)
	}
	if len(seen) != 10 {
		t.Fatalf("seen %d", len(seen))
	}
	if scheduler.Active() != 0 || scheduler.Deferred() != 0 || scheduler.Pending() != 0 {
		t.Fatalf("accounting: %d %d %d", scheduler.Active(), scheduler.Deferred(), scheduler.Pending())
	}
}

func TestSchedulerRequeueKeepsActive(t *testing.T) {
	scheduler := NewScheduler(32)
	j := job("multi", 1, 0, 2)
	scheduler.Submit(j)
	first := scheduler.Next()
	if first != j {
		t.Fatal("wrong job")
	}
	scheduler.Complete(first, true)
	if scheduler.Active() != 1 {
		t.Fatalf("active after requeue %d", scheduler.Active())
	}
	again := scheduler.Next()
	if again != j {
		t.Fatal("requeued job not next")
	}
	scheduler.Complete(again, false)
	if scheduler.Active() != 0 {
		t.Fatalf("active after finish %d", scheduler.Active())
	}
	if scheduler.Next() != nil {
		t.Fatal("unexpected job")
	}
}

func TestSchedulerCancelGeneration(t *testing.T) {
	scheduler := NewScheduler(32)
	scheduler.Submit(job("old1", 1, 0, 1))
	scheduler.Submit(job("old2", 1, 0, 1))
	inflight := scheduler.Next()
	scheduler.Submit(job("new", 2, 0, 1))
	scheduler.CancelGeneration(1)
	if inflight == nil || inflight.Ref.Generation != 1 {
		t.Fatal("inflight lost")
	}
	if scheduler.Active() != 2 { // inflight old + new
		t.Fatalf("active %d", scheduler.Active())
	}
	scheduler.Complete(inflight, false)
	next := scheduler.Next()
	if next == nil || next.Ref.Message.ID != "new" {
		t.Fatalf("%+v", next)
	}
}
