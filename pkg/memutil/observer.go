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

package memutil

import "sync/atomic"

// MappingObserver is told about changes to host mappings of memory that a
// platform can map into application address spaces. A platform that keeps
// its own references to host mappings (such as a hypervisor's stage-2
// mappings, which do not follow changes to host mappings) must drop them
// before the host mapping changes.
type MappingObserver interface {
	// WillUnmap is called before [addr, addr+length) is unmapped. After it
	// returns, the observer must not access the range.
	WillUnmap(addr, length uintptr)

	// WillReplace is called before [addr, addr+length) is replaced in place
	// by a new mapping of the same memory (mmap with MAP_FIXED), for example
	// to add PROT_WRITE. After the call returns, the observer must not use
	// its references to the old mapping, but it can map the new one.
	WillReplace(addr, length uintptr)
}

type observerHolder struct {
	MappingObserver
}

var observer atomic.Pointer[observerHolder]

// SetMappingObserver sets the MappingObserver of the process. It must be
// called before memory that the observer must know about is mapped.
func SetMappingObserver(o MappingObserver) {
	observer.Store(&observerHolder{o})
}

// WillUnmap tells the MappingObserver, if any, that [addr, addr+length) will
// be unmapped. The owner of a host mapping that can be mapped into an
// application address space must call it before it unmaps the range.
func WillUnmap(addr, length uintptr) {
	if o := observer.Load(); o != nil {
		o.WillUnmap(addr, length)
	}
}

// WillReplace tells the MappingObserver, if any, that [addr, addr+length)
// will be replaced in place by a new mapping. The owner of a host mapping
// that can be mapped into an application address space must call it before
// it replaces the mapping.
func WillReplace(addr, length uintptr) {
	if o := observer.Load(); o != nil {
		o.WillReplace(addr, length)
	}
}
