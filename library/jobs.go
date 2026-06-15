package library

import (
	"context"
	"errors"
	"log"
	"sync"

	"github.com/google/uuid"
)

// ErrJobCanceled is returned by a job's run function when the job was stopped by
// an admin from the Jobs page. The job manager records it as the failure
// message, so a canceled run shows "interrupted by user" rather than a generic
// error.
var ErrJobCanceled = errors.New("interrupted by user")

// JobManager runs background jobs (library scans, thumbnail regeneration) one
// at a time and records each run to the store so the admin Jobs page can show
// the active job's live progress plus a history of recent runs.
//
// Jobs are serialized through a single worker goroutine: starting a job while
// another is running enqueues it. This keeps CPU pressure predictable (scans
// and thumbnail generation are heavy) and avoids two jobs fighting over the
// single SQLite write connection.
type JobManager struct {
	store *Store

	mu       sync.Mutex
	queue    []*jobTask
	running  bool
	active   *activeJob // the currently running job, for live progress
	progress int        // persisted-progress throttle counter
}

type jobTask struct {
	id      string
	jobType string
	target  string
	run     func(p *JobProgress) error
}

// activeJob is the in-memory snapshot of the currently running job, updated far
// more frequently than we persist to the DB (progress is flushed periodically).
type activeJob struct {
	id      string
	jobType string
	target  string
	phase   string
	done    int
	total   int
	current string // file/item currently being processed, for live debugging
	// cancel stops the running job; it cancels the context handed to the job's
	// run function so cooperative work (e.g. a scan's per-photo loop) can bail.
	cancel context.CancelFunc
}

// JobProgress is handed to a job's run function so it can report progress and
// observe cancellation (via Canceled / the embedded context).
type JobProgress struct {
	mgr *JobManager
	id  string
	ctx context.Context
}

// Canceled reports whether the admin requested this job be stopped. Long-running
// work should poll this periodically and return ErrJobCanceled to abort
// promptly. A JobProgress with no context (jp == nil paths) is never canceled.
func (p *JobProgress) Canceled() bool {
	if p == nil || p.ctx == nil {
		return false
	}
	return p.ctx.Err() != nil
}

// NewJobManager builds a job manager backed by the given store.
func NewJobManager(store *Store) *JobManager {
	return &JobManager{store: store}
}

// Recover marks any jobs left "running" from a previous process as failed, so a
// crash/restart doesn't leave a phantom active job in the history.
func (m *JobManager) Recover() {
	if m.store == nil {
		return
	}
	if err := m.store.MarkStaleJobsFailed(); err != nil {
		log.Printf("jobs: recover stale: %v", err)
	}
}

// Enqueue schedules a job and returns its ID. The run function receives a
// JobProgress it should use to report phase/done/total as work proceeds.
func (m *JobManager) Enqueue(jobType, target string, run func(p *JobProgress) error) string {
	id := uuid.NewString()
	if m.store != nil {
		if err := m.store.CreateJob(id, jobType, target); err != nil {
			log.Printf("jobs: create %s: %v", jobType, err)
		}
	}
	m.mu.Lock()
	m.queue = append(m.queue, &jobTask{id: id, jobType: jobType, target: target, run: run})
	if !m.running {
		m.running = true
		go m.loop()
	}
	m.mu.Unlock()
	return id
}

// Cancel stops a job by ID. If the job is currently running, its context is
// canceled so the run function can bail out cooperatively (the loop then records
// it as failed with ErrJobCanceled). If the job is still queued, it is removed
// from the queue and marked failed immediately. Returns true if a matching
// pending/running job was found.
func (m *JobManager) Cancel(id string) bool {
	m.mu.Lock()
	if m.active != nil && m.active.id == id {
		cancel := m.active.cancel
		m.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		return true
	}
	// Not the active job: look for it in the queue and drop it.
	for i, task := range m.queue {
		if task.id == id {
			m.queue = append(m.queue[:i], m.queue[i+1:]...)
			m.mu.Unlock()
			if m.store != nil {
				if err := m.store.FinishJob(id, JobFailed, ErrJobCanceled.Error()); err != nil {
					log.Printf("jobs: cancel queued %s: %v", id, err)
				}
			}
			return true
		}
	}
	m.mu.Unlock()
	return false
}

func (m *JobManager) loop() {
	for {
		m.mu.Lock()
		if len(m.queue) == 0 {
			m.running = false
			m.active = nil
			m.mu.Unlock()
			return
		}
		task := m.queue[0]
		m.queue = m.queue[1:]
		ctx, cancel := context.WithCancel(context.Background())
		m.active = &activeJob{id: task.id, jobType: task.jobType, target: task.target, cancel: cancel}
		m.mu.Unlock()

		p := &JobProgress{mgr: m, id: task.id, ctx: ctx}
		err := task.run(p)
		cancel()
		// A canceled context means the stop was admin-initiated; normalize the
		// failure message even if the run function returned its own error.
		if ctx.Err() != nil {
			err = ErrJobCanceled
		}

		status, msg := JobSuccess, ""
		if err != nil {
			status, msg = JobFailed, err.Error()
		}
		if m.store != nil {
			// Flush the final progress numbers, then mark the job finished.
			m.flushProgress(task.id)
			if ferr := m.store.FinishJob(task.id, status, msg); ferr != nil {
				log.Printf("jobs: finish %s: %v", task.id, ferr)
			}
		}
	}
}

// set updates the active job's live progress and periodically persists it so
// the UI sees movement even on long phases, without a DB write per item.
func (p *JobProgress) set(phase string, done, total int) {
	m := p.mgr
	m.mu.Lock()
	if m.active != nil && m.active.id == p.id {
		m.active.phase = phase
		m.active.done = done
		m.active.total = total
	}
	m.progress++
	flush := m.progress%50 == 0
	m.mu.Unlock()
	if flush {
		m.flushProgress(p.id)
	}
}

// SetPhase records the current phase and resets the counters for it. The
// current-item marker is cleared so a new phase doesn't show a stale file.
func (p *JobProgress) SetPhase(phase string, total int) {
	p.SetCurrent("")
	p.set(phase, 0, total)
}

// SetCurrent records the item (e.g. photo path) currently being processed so
// the Jobs UI can show what a long phase is working on right now. It is a cheap
// in-memory update only (no DB flush): the value is surfaced via List()'s live
// overlay and is naturally ephemeral.
func (p *JobProgress) SetCurrent(item string) {
	m := p.mgr
	m.mu.Lock()
	if m.active != nil && m.active.id == p.id {
		m.active.current = item
	}
	m.mu.Unlock()
}

// SetProgress records absolute progress within the current phase.
func (p *JobProgress) SetProgress(done, total int) {
	m := p.mgr
	m.mu.Lock()
	phase := ""
	if m.active != nil && m.active.id == p.id {
		phase = m.active.phase
	}
	m.mu.Unlock()
	p.set(phase, done, total)
}

func (m *JobManager) flushProgress(id string) {
	if m.store == nil {
		return
	}
	m.mu.Lock()
	var phase string
	var done, total int
	if m.active != nil && m.active.id == id {
		phase, done, total = m.active.phase, m.active.done, m.active.total
	}
	m.mu.Unlock()
	if err := m.store.UpdateJobProgress(id, phase, done, total); err != nil {
		log.Printf("jobs: progress %s: %v", id, err)
	}
}

// List returns the recorded jobs (most recent first) with the live progress of
// the currently running job overlaid, so the UI reflects in-flight movement
// without waiting for the next periodic flush.
func (m *JobManager) List() ([]*Job, error) {
	if m.store == nil {
		return []*Job{}, nil
	}
	jobs, err := m.store.ListJobs()
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	active := m.active
	m.mu.Unlock()
	if active != nil {
		for _, j := range jobs {
			if j.ID == active.id {
				j.Phase = active.phase
				j.Done = active.done
				j.Total = active.total
				j.Current = active.current
			}
		}
	}
	return jobs, nil
}
