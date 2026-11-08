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

package boot

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func safeReadDir(path string) ([]string, error) {
	fd, err := unix.Openat(-1, path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("failed to open directory %s: %w", path, err)
	}
	defer unix.Close(fd)

	var names []string
	buf := make([]byte, 4096)
	for {
		n, err := unix.Getdents(fd, buf)
		if err != nil {
			return nil, fmt.Errorf("failed to get dents for %s: %w", path, err)
		}
		if n == 0 {
			break
		}
		var count int
		_, count, names = unix.ParseDirent(buf[:n], -1, names)
		if count == 0 {
			break
		}
	}
	var filtered []string
	for _, name := range names {
		if name != "." && name != ".." {
			filtered = append(filtered, name)
		}
	}
	return filtered, nil
}
