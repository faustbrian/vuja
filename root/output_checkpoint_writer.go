package root

import (
	"sync"
	"time"
)

type outputCheckpointSnapshot struct {
	rows    []string
	savedAt time.Time
}

// At most one immutable snapshot is being written and one newer snapshot is
// queued. Replacing the queued snapshot bounds work during slow storage.
type outputCheckpointWriter struct {
	mu       sync.Mutex
	ready    *sync.Cond
	pending  *outputCheckpointSnapshot
	stopping bool
	done     chan struct{}
	save     func(outputCheckpointSnapshot)
}

func newOutputCheckpointWriter(save func(outputCheckpointSnapshot)) *outputCheckpointWriter {
	w := &outputCheckpointWriter{save: save, done: make(chan struct{})}
	w.ready = sync.NewCond(&w.mu)
	go w.run()
	return w
}

func (w *outputCheckpointWriter) submit(snapshot outputCheckpointSnapshot) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.stopping {
		return
	}
	w.pending = &snapshot
	w.ready.Signal()
}

func (w *outputCheckpointWriter) stop(drain bool) {
	w.mu.Lock()
	w.stopping = true
	if !drain {
		w.pending = nil
	}
	w.ready.Broadcast()
	w.mu.Unlock()
	<-w.done
}

func (w *outputCheckpointWriter) run() {
	defer close(w.done)
	for {
		w.mu.Lock()
		for w.pending == nil && !w.stopping {
			w.ready.Wait()
		}
		if w.pending == nil {
			w.mu.Unlock()
			return
		}
		snapshot := *w.pending
		w.pending = nil
		w.mu.Unlock()
		w.save(snapshot)
	}
}
