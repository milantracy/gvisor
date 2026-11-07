// Copyright 2026 The gVisor Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package fdnotifier

import (
	"testing"
	"time"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/pkg/waiter"
)

func waitNotified(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatalf("no notification for %s", what)
	}
}

func TestNotifyReadableAndWritable(t *testing.T) {
	var p [2]int
	if err := unix.Pipe(p[:]); err != nil {
		t.Fatal(err)
	}
	defer unix.Close(p[0])
	defer unix.Close(p[1])
	for _, fd := range p {
		if err := unix.SetNonblock(fd, true); err != nil {
			t.Fatal(err)
		}
	}

	// Readable: register, then write to the pipe.
	var rq waiter.Queue
	rentry, rch := waiter.NewChannelEntry(waiter.ReadableEvents)
	rq.EventRegister(&rentry)
	defer rq.EventUnregister(&rentry)
	if err := AddFD(int32(p[0]), &rq); err != nil {
		t.Fatalf("AddFD(read end): %v", err)
	}
	defer RemoveFD(int32(p[0]))
	if got := NonBlockingPoll(int32(p[0]), waiter.ReadableEvents); got != 0 {
		t.Errorf("NonBlockingPoll(empty pipe) = %#x, want 0", got)
	}
	if _, err := unix.Write(p[1], []byte{1}); err != nil {
		t.Fatal(err)
	}
	waitNotified(t, rch, "readable pipe")
	if got := NonBlockingPoll(int32(p[0]), waiter.ReadableEvents); got&waiter.EventIn == 0 {
		t.Errorf("NonBlockingPoll(non-empty pipe) = %#x, want EventIn", got)
	}

	// Writable: the write end of a pipe with space is writable at once.
	var wq waiter.Queue
	wentry, wch := waiter.NewChannelEntry(waiter.WritableEvents)
	wq.EventRegister(&wentry)
	defer wq.EventUnregister(&wentry)
	if err := AddFD(int32(p[1]), &wq); err != nil {
		t.Fatalf("AddFD(write end): %v", err)
	}
	defer RemoveFD(int32(p[1]))
	if !HasFD(int32(p[1])) {
		t.Errorf("HasFD(write end) = false after AddFD")
	}
	waitNotified(t, wch, "writable pipe")
}
