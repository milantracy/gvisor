// Copyright 2026 The gVisor Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//	http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package hostinet

// SockOpt is used to generate get/setsockopt handlers and filters.
type SockOpt struct {
	// Level the socket option applies to.
	Level uint64
	// Name of the option.
	Name uint64
	// Size of the parameter. A size of 0 indicates that any size is
	// allowed (used for string or other variable-length types).
	Size uint64
	// Support getsockopt on this option.
	AllowGet bool
	// Support setsockopt on this option.
	AllowSet bool
	// AllowTruncatedGet allows getsockopt with optlen < Size.
	AllowTruncatedGet bool
}
