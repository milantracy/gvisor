// Copyright 2019 The gVisor Authors.
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

package gofer

import (
	"time"

	"gvisor.dev/gvisor/pkg/context"
	"gvisor.dev/gvisor/pkg/errors/linuxerr"
	"gvisor.dev/gvisor/pkg/waiter"
)

func blockUntilNonblockingPipeHasWriter(ctx context.Context, fd int32) error {
	for {
		ok, err := nonblockingPipeHasWriter(fd)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
		// Delay before trying again.
		if sleepErr := sleepBetweenNamedPipeOpenChecks(ctx); sleepErr != nil {
			// Another application thread may have opened this pipe for
			// writing, succeeded because we previously opened the pipe for
			// reading, and subsequently interrupted us for checkpointing (e.g.
			// this occurs in mknod tests under cooperative save/restore). In
			// this case, our open has to succeed for the checkpoint to include
			// a readable FD for the pipe, which is in turn necessary to
			// restore the other thread's writable FD for the same pipe
			// (otherwise it will get ENXIO). So we have to check
			// nonblockingPipeHasWriter() once last time.
			ok, err := nonblockingPipeHasWriter(fd)
			if err != nil {
				return err
			}
			if ok {
				return nil
			}
			return sleepErr
		}
	}
}

func sleepBetweenNamedPipeOpenChecks(ctx context.Context) error {
	var q waiter.NeverReady
	left, ok := ctx.BlockWithTimeoutOn(&q, waiter.EventIn, 100*time.Millisecond)
	if !ok && left != 0 {
		return linuxerr.ErrInterrupted
	}
	return nil
}
