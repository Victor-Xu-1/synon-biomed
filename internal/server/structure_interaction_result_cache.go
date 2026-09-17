package server

import (
	"bytes"
	"container/list"
	"context"
	"log"
	"net/http"
	"sync"
	"time"
)

const (
	structureInteractionCacheTTL      = 15 * time.Minute
	structureInteractionCacheEntries  = 32
	structureInteractionCacheMaxBytes = 64 << 20
	structureInteractionMaxWorkers    = 2
	structureInteractionMaxWorkItems  = 4
	structureInteractionMaxWorkBytes  = 48 << 20
)

type structureInteractionHTTPResult struct {
	Status int
	Header http.Header
	Body   []byte
}

func (r structureInteractionHTTPResult) sizeBytes() int {
	size := len(r.Body)
	for key, values := range r.Header {
		size += len(key)
		for _, value := range values {
			size += len(value)
		}
	}
	return size
}

type structureInteractionCacheEntry struct {
	key       string
	result    structureInteractionHTTPResult
	expiresAt time.Time
	size      int
}

type structureInteractionFlight struct {
	done      chan struct{}
	result    structureInteractionHTTPResult
	workBytes int
}

// structureInteractionResultCoordinator is the single process authority for
// expensive interaction-report work. It deduplicates identical work, limits
// task-owned kernels, and retains only successful bounded results.
type structureInteractionResultCoordinator struct {
	mu         sync.Mutex
	entries    map[string]*list.Element
	lru        *list.List
	inflight   map[string]*structureInteractionFlight
	workers    chan struct{}
	maxEntries int
	maxBytes   int
	maxWork    int
	maxWorkMem int
	ttl        time.Duration
	bytes      int
	workMem    int
}

func newStructureInteractionResultCoordinator(
	maxEntries int,
	maxBytes int,
	ttl time.Duration,
	maxWorkers int,
	maxWork int,
	maxWorkBytes int,
) *structureInteractionResultCoordinator {
	if maxEntries < 1 {
		maxEntries = 1
	}
	if maxBytes < 1 {
		maxBytes = 1
	}
	if ttl <= 0 {
		ttl = time.Minute
	}
	if maxWorkers < 1 {
		maxWorkers = 1
	}
	if maxWork < maxWorkers {
		maxWork = maxWorkers
	}
	if maxWorkBytes < 1 {
		maxWorkBytes = 1
	}
	return &structureInteractionResultCoordinator{
		entries:    make(map[string]*list.Element),
		lru:        list.New(),
		inflight:   make(map[string]*structureInteractionFlight),
		workers:    make(chan struct{}, maxWorkers),
		maxEntries: maxEntries,
		maxBytes:   maxBytes,
		maxWork:    maxWork,
		maxWorkMem: maxWorkBytes,
		ttl:        ttl,
	}
}

var structureInteractionResults = newStructureInteractionResultCoordinator(
	structureInteractionCacheEntries,
	structureInteractionCacheMaxBytes,
	structureInteractionCacheTTL,
	structureInteractionMaxWorkers,
	structureInteractionMaxWorkItems,
	structureInteractionMaxWorkBytes,
)

// Do returns false when only this HTTP waiter was cancelled. The shared work
// deliberately continues within its own timeout so a subsequent view can reuse
// the result instead of restarting the scientific kernel.
func (c *structureInteractionResultCoordinator) Do(
	waitContext context.Context,
	key string,
	workBytes int,
	workTimeout time.Duration,
	work func(context.Context) structureInteractionHTTPResult,
) (structureInteractionHTTPResult, bool) {
	now := time.Now()
	c.mu.Lock()
	c.removeExpiredLocked(now)
	if element := c.entries[key]; element != nil {
		entry := element.Value.(*structureInteractionCacheEntry)
		c.lru.MoveToFront(element)
		result := cloneStructureInteractionHTTPResult(entry.result)
		c.mu.Unlock()
		return result, true
	}
	flight := c.inflight[key]
	if flight == nil {
		workBytes = max(0, workBytes)
		if len(c.inflight) >= c.maxWork || workBytes > c.maxWorkMem-c.workMem {
			c.mu.Unlock()
			return structureInteractionOverloadedResult(), true
		}
		flight = &structureInteractionFlight{done: make(chan struct{}), workBytes: workBytes}
		c.inflight[key] = flight
		c.workMem += workBytes
		go c.run(key, flight, workTimeout, work)
	}
	done := flight.done
	c.mu.Unlock()

	select {
	case <-waitContext.Done():
		return structureInteractionHTTPResult{}, false
	case <-done:
		return cloneStructureInteractionHTTPResult(flight.result), true
	}
}

func (c *structureInteractionResultCoordinator) run(
	key string,
	flight *structureInteractionFlight,
	workTimeout time.Duration,
	work func(context.Context) structureInteractionHTTPResult,
) {
	workContext, cancel := context.WithTimeout(context.Background(), workTimeout)
	defer cancel()
	select {
	case c.workers <- struct{}{}:
		defer func() { <-c.workers }()
	case <-workContext.Done():
		c.finish(key, flight, structureInteractionHTTPResult{
			Status: http.StatusGatewayTimeout,
			Header: http.Header{"Content-Type": []string{"application/json"}},
			Body: []byte(
				`{"ok":false,"status":"failed","code":"interaction_queue_timeout","message":"interaction analysis remained queued beyond its bounded timeout","retryable":true}`,
			),
		})
		return
	}
	result := runStructureInteractionWork(workContext, work)
	if result.Status == 0 {
		result.Status = http.StatusOK
	}
	c.finish(key, flight, result)
}

func runStructureInteractionWork(
	workContext context.Context,
	work func(context.Context) structureInteractionHTTPResult,
) (result structureInteractionHTTPResult) {
	defer func() {
		recovered := recover()
		if recovered == nil {
			return
		}
		log.Printf("structure interaction worker panic: %v", recovered)
		result = structureInteractionHTTPResult{
			Status: http.StatusInternalServerError,
			Header: http.Header{"Content-Type": []string{"application/json"}},
			Body: []byte(
				`{"ok":false,"status":"failed","code":"interaction_worker_failed","message":"the interaction worker failed unexpectedly","retryable":true}`,
			),
		}
	}()
	return work(workContext)
}

func (c *structureInteractionResultCoordinator) finish(
	key string,
	flight *structureInteractionFlight,
	result structureInteractionHTTPResult,
) {
	c.mu.Lock()
	defer c.mu.Unlock()
	flight.result = cloneStructureInteractionHTTPResult(result)
	delete(c.inflight, key)
	c.workMem -= flight.workBytes
	if result.Status >= http.StatusOK && result.Status < http.StatusMultipleChoices {
		c.insertLocked(key, result, time.Now())
	}
	close(flight.done)
}

func structureInteractionOverloadedResult() structureInteractionHTTPResult {
	return structureInteractionHTTPResult{
		Status: http.StatusTooManyRequests,
		Header: http.Header{
			"Content-Type": []string{"application/json"},
			"Retry-After":  []string{"2"},
		},
		Body: []byte(
			`{"ok":false,"status":"failed","code":"interaction_capacity_exhausted","message":"interaction analysis capacity is temporarily full","retryable":true}`,
		),
	}
}

func (c *structureInteractionResultCoordinator) insertLocked(
	key string,
	result structureInteractionHTTPResult,
	now time.Time,
) {
	size := result.sizeBytes()
	if size > c.maxBytes {
		return
	}
	if existing := c.entries[key]; existing != nil {
		c.removeElementLocked(existing)
	}
	entry := &structureInteractionCacheEntry{
		key:       key,
		result:    cloneStructureInteractionHTTPResult(result),
		expiresAt: now.Add(c.ttl),
		size:      size,
	}
	element := c.lru.PushFront(entry)
	c.entries[key] = element
	c.bytes += size
	for c.lru.Len() > c.maxEntries || c.bytes > c.maxBytes {
		c.removeElementLocked(c.lru.Back())
	}
}

func (c *structureInteractionResultCoordinator) removeExpiredLocked(now time.Time) {
	for element := c.lru.Back(); element != nil; {
		previous := element.Prev()
		entry := element.Value.(*structureInteractionCacheEntry)
		if !entry.expiresAt.After(now) {
			c.removeElementLocked(element)
		}
		element = previous
	}
}

func (c *structureInteractionResultCoordinator) removeElementLocked(element *list.Element) {
	if element == nil {
		return
	}
	entry := element.Value.(*structureInteractionCacheEntry)
	delete(c.entries, entry.key)
	c.bytes -= entry.size
	c.lru.Remove(element)
}

func cloneStructureInteractionHTTPResult(result structureInteractionHTTPResult) structureInteractionHTTPResult {
	return structureInteractionHTTPResult{
		Status: result.Status,
		Header: result.Header.Clone(),
		Body:   bytes.Clone(result.Body),
	}
}

type structureInteractionCaptureWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newStructureInteractionCaptureWriter() *structureInteractionCaptureWriter {
	return &structureInteractionCaptureWriter{header: make(http.Header)}
}

func (w *structureInteractionCaptureWriter) Header() http.Header {
	return w.header
}

func (w *structureInteractionCaptureWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *structureInteractionCaptureWriter) Write(content []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.body.Write(content)
}

func (w *structureInteractionCaptureWriter) Result() structureInteractionHTTPResult {
	status := w.status
	if status == 0 {
		status = http.StatusOK
	}
	return structureInteractionHTTPResult{
		Status: status,
		Header: w.header.Clone(),
		Body:   bytes.Clone(w.body.Bytes()),
	}
}

func writeStructureInteractionHTTPResult(w http.ResponseWriter, result structureInteractionHTTPResult) {
	for key, values := range result.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(result.Status)
	_, _ = w.Write(result.Body)
}
