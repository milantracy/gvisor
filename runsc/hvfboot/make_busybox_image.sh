#!/bin/bash
# Copyright 2026 The gVisor Authors.
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#     http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.

# Makes an EROFS image with Debian's static arm64 busybox, with 16 KiB
# blocks and no inline file data (gVisor maps file data directly from the
# image), for hvfboot. Needs Docker.
#
# Debian's mkfs.erofs allows blocks up to the page size of its build machine
# (4 KiB), so the script builds erofs-utils with a larger MAX_BLOCK_SIZE.
#
#   make_busybox_image.sh <output.erofs>

set -euo pipefail

out="$(cd "$(dirname "$1")" && pwd)/$(basename "$1")"
erofs_utils=1.8.6
docker run --rm --platform linux/arm64 -e DEBIAN_FRONTEND=noninteractive \
  -v "$(dirname "${out}"):/out" debian:trixie sh -euc "
  apt-get update -qq >/dev/null
  apt-get install -y -qq busybox-static binutils build-essential autoconf \
    automake libtool pkg-config uuid-dev curl ca-certificates >/dev/null
  curl -fsSL https://git.kernel.org/pub/scm/linux/kernel/git/xiang/erofs-utils.git/snapshot/erofs-utils-${erofs_utils}.tar.gz | tar -xz -C /root
  cd /root/erofs-utils-${erofs_utils}
  ./autogen.sh >/dev/null 2>&1
  ./configure -q MAX_BLOCK_SIZE=65536 --disable-lz4 --disable-lzma \
    --without-zlib --without-libdeflate --without-libzstd --without-qpl \
    --without-xxhash --without-selinux --disable-fuse >/dev/null
  make -s -j\$(nproc) >/dev/null 2>&1
  export PATH=/root/erofs-utils-${erofs_utils}/mkfs:\$PATH
  readelf -lW /bin/busybox | grep -E 'LOAD' >&2
  mkdir -p /root/fs/bin /root/fs/sbin /root/fs/proc /root/fs/dev /root/fs/tmp
  cp /bin/busybox /root/fs/bin/busybox
  for applet in sh echo cat ls dmesg uname true false; do
    ln -s busybox /root/fs/bin/\${applet}
  done
  mkfs.erofs -b 16384 -E noinline_data /out/$(basename "${out}") /root/fs >&2
"
echo "wrote ${out}"
