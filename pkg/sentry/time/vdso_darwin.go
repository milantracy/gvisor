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

package time

import "golang.org/x/sys/unix"

// vdsoClockGettime calls clock_gettime in libSystem. It returns 0 on success
// or a negative errno value on failure.
func vdsoClockGettime(clockid ClockID, ts *unix.Timespec) int {
	var id int32
	switch clockid {
	case Realtime:
		id = unix.CLOCK_REALTIME
	case Monotonic:
		id = unix.CLOCK_MONOTONIC
	case MonotonicRaw:
		id = unix.CLOCK_MONOTONIC_RAW
	default:
		return -int(unix.EINVAL)
	}
	if err := unix.ClockGettime(id, ts); err != nil {
		return -int(err.(unix.Errno))
	}
	return 0
}
