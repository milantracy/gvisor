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

//go:build linux

package p9

import "syscall"

// StatToAttr converts a Linux syscall stat structure to an Attr.
func StatToAttr(s *syscall.Stat_t, req AttrMask) (Attr, AttrMask) {
	attr := Attr{
		UID: NoUID,
		GID: NoGID,
	}
	if req.Mode {
		// p9.FileMode corresponds to Linux mode_t.
		attr.Mode = FileMode(s.Mode)
	}
	if req.NLink {
		attr.NLink = uint64(s.Nlink)
	}
	if req.UID {
		attr.UID = UID(s.Uid)
	}
	if req.GID {
		attr.GID = GID(s.Gid)
	}
	if req.RDev {
		attr.RDev = s.Rdev
	}
	if req.ATime {
		attr.ATimeSeconds = uint64(s.Atim.Sec)
		attr.ATimeNanoSeconds = uint64(s.Atim.Nsec)
	}
	if req.MTime {
		attr.MTimeSeconds = uint64(s.Mtim.Sec)
		attr.MTimeNanoSeconds = uint64(s.Mtim.Nsec)
	}
	if req.CTime {
		attr.CTimeSeconds = uint64(s.Ctim.Sec)
		attr.CTimeNanoSeconds = uint64(s.Ctim.Nsec)
	}
	if req.Size {
		attr.Size = uint64(s.Size)
	}
	if req.Blocks {
		attr.BlockSize = uint64(s.Blksize)
		attr.Blocks = uint64(s.Blocks)
	}

	// Use the req field because we already have it.
	req.BTime = false
	req.Gen = false
	req.DataVersion = false

	return attr, req
}
