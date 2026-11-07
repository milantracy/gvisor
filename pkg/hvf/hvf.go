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

// Package hvf is a binding of Apple's Hypervisor.framework for arm64 that
// does not use cgo. Each function is called through a libSystem-style
// trampoline, as the Go runtime and golang.org/x/sys/unix call libSystem on
// darwin.
//
// A process can have one VM. A vCPU belongs to the thread that creates it;
// only that thread can run it or access its registers, so callers lock the
// goroutine to its thread with runtime.LockOSThread. ExitVCPUs is the only
// call that is allowed from another thread.
//
// The process must have the com.apple.security.hypervisor entitlement.
// Without it, CreateVM returns ErrDenied.
//
// The package has code only on darwin/arm64.
package hvf
