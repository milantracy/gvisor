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

//go:build darwin

package state

import (
	"fmt"
	"time"

	"golang.org/x/sys/unix"
)

// CPUTime returns the CPU time usage by Sentry and app.
//
// On darwin, the application runs on vCPU threads of the Sentry process, so
// the process CPU time includes it.
func CPUTime() (time.Duration, error) {
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_PROCESS_CPUTIME_ID, &ts); err != nil {
		return 0, fmt.Errorf("failed calling clock_gettime(CLOCK_PROCESS_CPUTIME_ID): %w", err)
	}
	return time.Duration(ts.Nano()), nil
}
