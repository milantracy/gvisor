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

package gofer

import (
	"gvisor.dev/gvisor/pkg/abi/linux"
	"gvisor.dev/gvisor/pkg/context"
	"gvisor.dev/gvisor/pkg/lisafs"
	"gvisor.dev/gvisor/pkg/sentry/kernel/auth"
	"gvisor.dev/gvisor/pkg/sentry/vfs"
)

// directfsInode is the inode implementation for directfs, which darwin does
// not support (see directfsSupported). No directfsInode exists on darwin; its
// methods only satisfy the type switches in inode_impl.go.
type directfsInode struct {
	inode

	controlFD     int
	controlFDLisa lisafs.ClientFD `state:"nosave"`
}

const errDirectfs = "gofer: directfs is not supported on darwin"

func tryOpen(open func(int) (int, error)) (int, error) { panic(errDirectfs) }

func (fs *filesystem) getDirectfsRootDentry(ctx context.Context, rootHostFD int, rootControlFD lisafs.ClientFD) (*dentry, error) {
	panic(errDirectfs)
}

func (i *directfsInode) openHandle(ctx context.Context, flags uint32, d *dentry) (handle, error) {
	panic(errDirectfs)
}

func (i *directfsInode) updateMetadataLocked(h handle) error { panic(errDirectfs) }

func (i *directfsInode) chmod(ctx context.Context, mode uint16, d *dentry) error {
	panic(errDirectfs)
}

func (i *directfsInode) prepareSetStat(ctx context.Context, stat *linux.Statx, d *dentry) error {
	panic(errDirectfs)
}

func (i *directfsInode) setStatLocked(ctx context.Context, stat *linux.Statx, d *dentry) (uint32, error) {
	panic(errDirectfs)
}

func (i *directfsInode) getHostChild(name string) (*dentry, error) { panic(errDirectfs) }

func (i *directfsInode) getXattr(ctx context.Context, name string, size uint64, d *dentry) (string, error) {
	panic(errDirectfs)
}

func (i *directfsInode) setXattr(ctx context.Context, opts *vfs.SetXattrOptions, d *dentry) error {
	panic(errDirectfs)
}

func (i *directfsInode) listXattr(ctx context.Context, size uint64, d *dentry) ([]string, error) {
	panic(errDirectfs)
}

func (i *directfsInode) removeXattr(ctx context.Context, name string, d *dentry) error {
	panic(errDirectfs)
}

func (i *directfsInode) mknod(ctx context.Context, name string, creds *auth.Credentials, opts *vfs.MknodOptions, d *dentry) (*dentry, error) {
	panic(errDirectfs)
}

func (i *directfsInode) link(target *dentry, name string, d *dentry) (*dentry, error) {
	panic(errDirectfs)
}

func (i *directfsInode) mkdir(name string, mode linux.FileMode, uid auth.KUID, gid auth.KGID, createDentry bool, d *dentry) (*dentry, error) {
	panic(errDirectfs)
}

func (i *directfsInode) symlink(name, target string, creds *auth.Credentials, d *dentry) (*dentry, error) {
	panic(errDirectfs)
}

func (i *directfsInode) openCreate(name string, accessFlags uint32, mode linux.FileMode, uid auth.KUID, gid auth.KGID, createDentry bool, d *dentry) (*dentry, handle, error) {
	panic(errDirectfs)
}

func (i *directfsInode) getDirentsLocked(recordDirent func(name string, key inoKey, dType uint8), d *dentry) error {
	panic(errDirectfs)
}

func (i *directfsInode) connect(ctx context.Context, sockType linux.SockType, euid lisafs.UID, egid lisafs.GID, groups []lisafs.GID, d *dentry) (int, error) {
	panic(errDirectfs)
}

func (i *directfsInode) destroy(ctx context.Context) { panic(errDirectfs) }

func doRevalidationDirectfs(ctx context.Context, vfsObj *vfs.VirtualFilesystem, state *revalidateState, ds **[]*dentry) error {
	panic(errDirectfs)
}

func (i *directfsInode) readlink() (string, error) { panic(errDirectfs) }

func (i *directfsInode) statfs() (linux.Statfs, error) { panic(errDirectfs) }

func (i *directfsInode) restoreFile(ctx context.Context, controlFD int, opts *vfs.CompleteRestoreOptions, d *dentry) error {
	panic(errDirectfs)
}
