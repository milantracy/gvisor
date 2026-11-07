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

package unet

// fdDir lists the open file descriptors of the process.
const fdDir = "/dev/fd"

// havePacket is true if AF_UNIX SOCK_SEQPACKET sockets are available.
const havePacket = false

// socketDir is the directory for socket files. os.TempDir() under Bazel is too
// long for the 104-byte sun_path on darwin, even as a relative path.
const socketDir = "/tmp"

// truncatedRightsClosed is true if the host closes SCM_RIGHTS file
// descriptors that do not fit in the receiver's control buffer. darwin installs all
// of them in the receiver anyway, sets MSG_CTRUNC, and leaves cmsg_len at the
// full length.
const truncatedRightsClosed = false
