package monitor

import (
	"container/heap"
	"context"

	"github.com/yuggr/transpane/internal/domain"
	"github.com/yuggr/transpane/internal/protection"
)

// SegmentResult is one completed segment of a job.
type SegmentResult struct {
	OK    bool
	Text  string
	Error string
}

// Job is one translation unit. Its segments are processed one at a time and
// the job is re-queued between segments so priority remains live.
type Job struct {
	Ref       domain.TaskRef
	Segments  []protection.Segment
	Priority  int
	Sequence  uint64
	NextIndex int
	Results   []SegmentResult
	Ctx       context.Context
	Cancel    context.CancelFunc
}

func (j *Job) cancel() {
	if j.Cancel != nil {
		j.Cancel()
	}
}

type jobHeap []*Job

func (h jobHeap) Len() int { return len(h) }
func (h jobHeap) Less(i, j int) bool {
	if h[i].Priority != h[j].Priority {
		return h[i].Priority < h[j].Priority
	}
	return h[i].Sequence < h[j].Sequence
}
func (h jobHeap) Swap(i, j int)   { h[i], h[j] = h[j], h[i] }
func (h *jobHeap) Push(value any) { *h = append(*h, value.(*Job)) }
func (h *jobHeap) Pop() any {
	old := *h
	last := old[len(old)-1]
	*h = old[:len(old)-1]
	return last
}

// Scheduler keeps active (queued + in-flight) jobs bounded while retaining
// deferred work in FIFO order. It is not safe for concurrent use; the monitor
// event loop owns it.
type Scheduler struct {
	max      int
	queue    jobHeap
	deferred []*Job
	inflight *Job
	active   int
	sequence uint64
}

func NewScheduler(max int) *Scheduler {
	if max <= 0 {
		max = 32
	}
	scheduler := &Scheduler{max: max}
	heap.Init(&scheduler.queue)
	return scheduler
}

// Submit enqueues a job or defers it. Deferred jobs do not count as active.
func (s *Scheduler) Submit(job *Job) bool {
	s.sequence++
	job.Sequence = s.sequence
	if s.active < s.max {
		heap.Push(&s.queue, job)
		s.active++
		return true
	}
	s.deferred = append(s.deferred, job)
	return false
}

// Next returns the highest-priority queued job when no job is in flight.
func (s *Scheduler) Next() *Job {
	if s.inflight != nil {
		return nil
	}
	s.promote()
	if len(s.queue) == 0 {
		return nil
	}
	job := heap.Pop(&s.queue).(*Job)
	s.inflight = job
	return job
}

// Complete releases the in-flight slot. requeue keeps the job active and
// makes it eligible for the next segment.
func (s *Scheduler) Complete(job *Job, requeue bool) {
	s.inflight = nil
	if requeue {
		heap.Push(&s.queue, job)
		return
	}
	if s.active > 0 {
		s.active--
	}
	s.promote()
}

// CancelGeneration drops queued and deferred jobs at or below generation. An
// in-flight job is left for its cancellation result and accounting release.
// DropWhere removes queued/deferred jobs matching match, cancels them and
// keeps active/deferred accounting consistent. An in-flight job is not removed
// here; the monitor cancels it separately and releases it on its result.
func (s *Scheduler) DropWhere(match func(*Job) bool) int {
	removed := 0
	kept := make(jobHeap, 0, len(s.queue))
	for _, job := range s.queue {
		if match(job) {
			job.cancel()
			if s.active > 0 {
				s.active--
			}
			removed++
			continue
		}
		kept = append(kept, job)
	}
	s.queue = kept
	heap.Init(&s.queue)
	deferred := make([]*Job, 0, len(s.deferred))
	for _, job := range s.deferred {
		if match(job) {
			job.cancel()
			removed++
			continue
		}
		deferred = append(deferred, job)
	}
	s.deferred = deferred
	s.promote()
	return removed
}

func (s *Scheduler) CancelGeneration(generation uint64) {
	kept := make(jobHeap, 0, len(s.queue))
	for _, job := range s.queue {
		if job.Ref.Generation <= generation {
			if s.active > 0 {
				s.active--
			}
			continue
		}
		kept = append(kept, job)
	}
	s.queue = kept
	heap.Init(&s.queue)

	deferred := make([]*Job, 0, len(s.deferred))
	for _, job := range s.deferred {
		if job.Ref.Generation <= generation {
			continue
		}
		deferred = append(deferred, job)
	}
	s.deferred = deferred
	s.promote()
}

func (s *Scheduler) promote() {
	for len(s.deferred) > 0 && s.active < s.max {
		job := s.deferred[0]
		s.deferred = s.deferred[1:]
		heap.Push(&s.queue, job)
		s.active++
	}
}

func (s *Scheduler) Active() int   { return s.active }
func (s *Scheduler) Deferred() int { return len(s.deferred) }
func (s *Scheduler) Pending() int  { return s.active + len(s.deferred) }

func (s *Scheduler) Inflight() *Job { return s.inflight }

// Queued reports the number of active jobs waiting (not in flight).
func (s *Scheduler) Queued() int { return len(s.queue) }
