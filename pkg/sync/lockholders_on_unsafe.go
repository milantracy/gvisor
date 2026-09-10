// Copyright 2026 The gVisor Authors.
//
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file or at
// https://developers.google.com/open-source/licenses/bsd.

//go:build lockholders
// +build lockholders

// This file implements RWMutex holder tracking, enabled by the "lockholders"
// build tag: outstanding acquisitions are indexed by mutex. Far too expensive
// for production.

package sync

import (
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"gvisor.dev/gvisor/pkg/goid"
)

const maxRecordedFrames = 24

// holderRecord describes a single outstanding acquisition of a mutex.
type holderRecord struct {
	goid     int64
	write    bool
	acquired time.Time
	pcs      [maxRecordedFrames]uintptr
	npcs     int
}

func (h *holderRecord) String(now time.Time) string {
	kind := "read"
	if h.write {
		kind = "write"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\t%s, goroutine %d, held for %v\n", kind, h.goid, now.Sub(h.acquired).Round(time.Second))
	frames := runtime.CallersFrames(h.pcs[:h.npcs])
	for {
		frame, more := frames.Next()
		fmt.Fprintf(&b, "\t\t%s\n\t\t\t%s:%d\n", frame.Function, frame.File, frame.Line)
		if !more {
			break
		}
	}
	return b.String()
}

// holderShard holds the records for a subset of mutexes. The stdlib sync.Mutex
// avoids recursing back into tracking.
type holderShard struct {
	mu sync.Mutex
	m  map[unsafe.Pointer][]*holderRecord
}

const numHolderShards = 64

var holderShards [numHolderShards]holderShard

// unmatchedReleases counts releases with no matching record.
var unmatchedReleases atomic.Int64

func shardFor(l unsafe.Pointer) *holderShard {
	return &holderShards[(uintptr(l)>>4)%numHolderShards]
}

func noteAcquire(l unsafe.Pointer, write bool) {
	rec := &holderRecord{
		goid:     goid.Get(),
		write:    write,
		acquired: time.Now(),
	}
	// Skip runtime.Callers, noteAcquire, and the noteHolder{R,W}Lock wrapper.
	rec.npcs = runtime.Callers(3, rec.pcs[:])

	s := shardFor(l)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = make(map[unsafe.Pointer][]*holderRecord)
	}
	s.m[l] = append(s.m[l], rec)
}

func noteRelease(l unsafe.Pointer, write bool) {
	id := goid.Get()

	s := shardFor(l)
	s.mu.Lock()
	defer s.mu.Unlock()

	recs := s.m[l]
	for i := len(recs) - 1; i >= 0; i-- {
		if recs[i].goid == id && recs[i].write == write {
			recs = append(recs[:i], recs[i+1:]...)
			if len(recs) == 0 {
				// Bound growth; a leaked reference keeps its entry alive.
				delete(s.m, l)
			} else {
				s.m[l] = recs
			}
			return
		}
	}
	unmatchedReleases.Add(1)
}

// noteHolderDowngrade converts this goroutine's write record on l to a read
// record.
func noteHolderDowngrade(l unsafe.Pointer) {
	id := goid.Get()

	s := shardFor(l)
	s.mu.Lock()
	defer s.mu.Unlock()

	recs := s.m[l]
	for i := len(recs) - 1; i >= 0; i-- {
		if recs[i].goid == id && recs[i].write {
			recs[i].write = false
			return
		}
	}
	unmatchedReleases.Add(1)
}

func noteHolderRLock(l unsafe.Pointer) {
	noteAcquire(l, false)
}

func noteHolderRUnlock(l unsafe.Pointer) {
	noteRelease(l, false)
}

func noteHolderWLock(l unsafe.Pointer) {
	noteAcquire(l, true)
}

func noteHolderWUnlock(l unsafe.Pointer) {
	noteRelease(l, true)
}

// DumpLockHolders returns every RWMutex currently held, with the stack that
// acquired it, oldest first.
func DumpLockHolders() string {
	now := time.Now()

	var all []*holderRecord
	addrs := make(map[*holderRecord]unsafe.Pointer)
	for i := range holderShards {
		s := &holderShards[i]
		s.mu.Lock()
		for l, recs := range s.m {
			for _, rec := range recs {
				all = append(all, rec)
				addrs[rec] = l
			}
		}
		s.mu.Unlock()
	}

	sort.Slice(all, func(i, j int) bool {
		return all[i].acquired.Before(all[j].acquired)
	})

	var b strings.Builder
	fmt.Fprintf(&b, "*** RWMutex holders (%d outstanding, oldest first) ***\n", len(all))
	if n := unmatchedReleases.Load(); n != 0 {
		fmt.Fprintf(&b, "WARNING: %d unmatched release(s) observed; records may be incomplete\n", n)
	}
	for _, rec := range all {
		fmt.Fprintf(&b, "\nRWMutex %p:\n%s", addrs[rec], rec.String(now))
	}
	return b.String()
}
