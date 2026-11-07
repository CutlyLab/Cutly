package scheduler

import (
	"container/heap"

	renderapi "github.com/CutlyLab/Cutly/render/api"
)

// jobIDHeap is an in-memory acceleration index. Durable scheduler truth stays
// in Store; the heap is rebuilt from replayed snapshots on Open.
type jobIDHeap []string

func (value jobIDHeap) Len() int           { return len(value) }
func (value jobIDHeap) Less(i, j int) bool { return value[i] < value[j] }
func (value jobIDHeap) Swap(i, j int)      { value[i], value[j] = value[j], value[i] }
func (value *jobIDHeap) Push(item any)     { *value = append(*value, item.(string)) }
func (value *jobIDHeap) Pop() any {
	items := *value
	last := len(items) - 1
	item := items[last]
	items[last] = ""
	*value = items[:last]
	return item
}

func newStateIndex() map[renderapi.JobState]*jobIDHeap {
	result := make(map[renderapi.JobState]*jobIDHeap, 6)
	for _, state := range []renderapi.JobState{
		renderapi.JobQueued, renderapi.JobRunning, renderapi.JobCanceling,
		renderapi.JobCanceled, renderapi.JobFailed, renderapi.JobComplete,
	} {
		items := &jobIDHeap{}
		heap.Init(items)
		result[state] = items
	}
	return result
}

func (value *Coordinator) rebuildStateIndex() {
	value.stateJobs = newStateIndex()
	for _, snapshot := range value.jobs {
		items := value.stateJobs[snapshot.State]
		*items = append(*items, snapshot.Job.ID)
	}
	for _, items := range value.stateJobs {
		heap.Init(items)
	}
}

func (value *Coordinator) indexSnapshotLocked(snapshot JobSnapshot) {
	items := value.stateJobs[snapshot.State]
	if items == nil {
		return
	}
	heap.Push(items, snapshot.Job.ID)
}

// firstSnapshotLocked lazily discards stale heap entries left by monotonic
// state transitions. Valid excluded entries are popped temporarily and then
// restored; the exclusion set is bounded by MaxSnapshotExclusions.
func (value *Coordinator) firstSnapshotLocked(state renderapi.JobState, excluded map[string]struct{}) (JobSnapshot, bool) {
	items := value.stateJobs[state]
	if items == nil {
		return JobSnapshot{}, false
	}
	skipped := make([]string, 0, len(excluded))
	defer func() {
		for _, jobID := range skipped {
			heap.Push(items, jobID)
		}
	}()
	for items.Len() > 0 {
		jobID := (*items)[0]
		snapshot, exists := value.jobs[jobID]
		if !exists || snapshot.State != state {
			heap.Pop(items)
			continue
		}
		if _, blocked := excluded[jobID]; blocked {
			skipped = append(skipped, heap.Pop(items).(string))
			continue
		}
		return snapshot, true
	}
	return JobSnapshot{}, false
}
