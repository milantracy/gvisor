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

# Makes an EROFS image for hvfboot from the root filesystem of a linux/arm64
# container image, with 16 KiB blocks and no inline file data (gVisor maps
# file data directly from the image). Needs Docker with BuildKit.
#
# Debian's mkfs.erofs allows blocks up to the page size of its build machine
# (4 KiB), so the script builds erofs-utils with a larger MAX_BLOCK_SIZE.
#
#   make_image.sh <output.erofs> <image>
#   make_image.sh python.erofs python:3.13-slim

set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 <output.erofs> <image>" >&2
  exit 2
fi
out="$(cd "$(dirname "$1")" && pwd)/$(basename "$1")"
image="$2"
erofs_utils=1.8.6
tmp="$(mktemp -d)"
trap 'rm -rf "${tmp}"' EXIT

docker build --platform linux/arm64 --build-arg "IMAGE=${image}" \
  --output "type=local,dest=${tmp}" -f - "${tmp}" <<EOF
ARG IMAGE
FROM \${IMAGE} AS root

FROM debian:trixie AS mkfs
RUN apt-get update -qq && DEBIAN_FRONTEND=noninteractive apt-get install -y -qq \
    build-essential autoconf automake libtool pkg-config uuid-dev curl \
    ca-certificates >/dev/null
RUN curl -fsSL https://git.kernel.org/pub/scm/linux/kernel/git/xiang/erofs-utils.git/snapshot/erofs-utils-${erofs_utils}.tar.gz | tar -xz -C /root
WORKDIR /root/erofs-utils-${erofs_utils}
RUN ./autogen.sh >/dev/null 2>&1 && \
    ./configure -q MAX_BLOCK_SIZE=65536 --disable-lz4 --disable-lzma \
      --without-zlib --without-libdeflate --without-libzstd --without-qpl \
      --without-xxhash --without-selinux --disable-fuse >/dev/null && \
    make -s -j\$(nproc) >/dev/null 2>&1
COPY --from=root / /rootfs
RUN mkdir -p /rootfs/proc /rootfs/dev /rootfs/tmp /rootfs/sys && \
    mkfs/mkfs.erofs -b 16384 -E noinline_data /image.erofs /rootfs

FROM scratch
COPY --from=mkfs /image.erofs /
EOF
mv "${tmp}/image.erofs" "${out}"
echo "wrote ${out}"
