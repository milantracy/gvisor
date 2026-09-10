// Copyright 2026 The gVisor Authors.
//
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file or at
// https://developers.google.com/open-source/licenses/bsd.

//go:build !lockholders
// +build !lockholders

package sync

import (
	"unsafe"
)

func noteHolderRLock(l unsafe.Pointer) {
}

func noteHolderRUnlock(l unsafe.Pointer) {
}

func noteHolderWLock(l unsafe.Pointer) {
}

func noteHolderWUnlock(l unsafe.Pointer) {
}

func noteHolderDowngrade(l unsafe.Pointer) {
}

// DumpLockHolders returns every RWMutex currently held. Only implemented under
// the "lockholders" build tag.
func DumpLockHolders() string {
	return "lock holder tracking is disabled; rebuild with --tags=lockholders to enable\n"
}
