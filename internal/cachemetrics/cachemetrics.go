package cachemetrics

import (
	"sort"
	"sync"
	"sync/atomic"
)

type Snapshot struct {
	Name      string `json:"name"`
	Hits      uint64 `json:"hits"`
	Misses    uint64 `json:"misses"`
	Evictions uint64 `json:"evictions"`
}

// Counter keeps hot-path accounting lock-free after one-time registration.
type Counter struct {
	name      string
	hits      atomic.Uint64
	misses    atomic.Uint64
	evictions atomic.Uint64
}

var registry struct {
	sync.Mutex
	counters []*Counter
}

func New(name string) *Counter {
	counter := &Counter{name: name}
	registry.Lock()
	registry.counters = append(registry.counters, counter)
	registry.Unlock()
	return counter
}

func (c *Counter) Hit()      { c.hits.Add(1) }
func (c *Counter) Miss()     { c.misses.Add(1) }
func (c *Counter) Eviction() { c.evictions.Add(1) }

func Snapshots() []Snapshot {
	registry.Lock()
	counters := append([]*Counter(nil), registry.counters...)
	registry.Unlock()
	result := make([]Snapshot, 0, len(counters))
	for _, counter := range counters {
		result = append(result, Snapshot{
			Name: counter.name, Hits: counter.hits.Load(), Misses: counter.misses.Load(), Evictions: counter.evictions.Load(),
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result
}
