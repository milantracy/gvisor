// Copyright 2018 The gVisor Authors.
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

package sandboxsetup

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
	"gvisor.dev/gvisor/runsc/config"
)

// PivotRoot changes the root filesystem to the given directory using
// pivot_root(2). The old root is unmounted after the pivot.
func PivotRoot(root string) error {
	if err := os.Chdir(root); err != nil {
		return fmt.Errorf("error changing working directory: %v", err)
	}
	// pivot_root(new_root, put_old) moves the root filesystem (old_root)
	// of the calling process to the directory put_old and makes new_root
	// the new root filesystem of the calling process.
	//
	// pivot_root(".", ".") makes a mount of the working directory the new
	// root filesystem, so it will be moved in "/" and then the old_root
	// will be moved to "/" too. The parent mount of the old_root will be
	// new_root, so after umounting the old_root, we will see only
	// the new_root in "/".
	if err := unix.PivotRoot(".", "."); err != nil {
		return fmt.Errorf("pivot_root failed, make sure that the root mount has a parent: %v", err)
	}

	if err := unix.Unmount(".", unix.MNT_DETACH); err != nil {
		return fmt.Errorf("error umounting the old root file system: %v", err)
	}
	return nil
}

// AdjustMountOptions adds filesystem-specific gofer mount options.
func AdjustMountOptions(conf *config.Config, path string, opts []string) ([]string, error) {
	rv := make([]string, len(opts))
	copy(rv, opts)

	statfs := unix.Statfs_t{}
	if err := unix.Statfs(path, &statfs); err != nil {
		return nil, err
	}
	switch statfs.Type {
	case unix.OVERLAYFS_SUPER_MAGIC:
		rv = append(rv, "overlayfs_stale_read")
	case unix.NFS_SUPER_MAGIC, unix.FUSE_SUPER_MAGIC:
		// The gofer client implements remote file handle sharing for
		// performance. However, remote filesystems like NFS and FUSE
		// rely on close(2) syscall for flushing file data to the
		// server. Such handle sharing prevents the application's
		// close(2) syscall from being propagated to the host. Hence
		// disable file handle sharing, so remote files are flushed
		// correctly.
		rv = append(rv, "disable_file_handle_sharing")
	}
	return rv, nil
}
