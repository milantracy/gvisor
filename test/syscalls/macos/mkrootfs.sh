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

# mkrootfs.sh OUT: builds the syscall test binaries of subset.txt for Linux
# (in the Docker build container of `make`), and makes OUT/rootfs: the
# ubuntu:22.04 image with the merged runfiles of the tests in /runfiles.
#
# The tests are dynamically linked against the glibc of the build container
# (Ubuntu 22.04), so the root filesystem is ubuntu:22.04.
set -euo pipefail

OUT="$(mkdir -p "$1" && cd "$1" && pwd)"
HERE="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "${HERE}/../../.." && pwd)"
cd "${REPO}"

TESTS=$(tr "\n" " " < "${HERE}/subset.txt")
TARGETS=$(for t in ${TESTS}; do printf "//test/syscalls/linux:%s " "${t}"; done)

# `make build` builds the targets, then fails to print their paths when
# there are many targets. Only the build result matters here.
make build TARGETS="${TARGETS}" >"${OUT}/build.log" 2>&1 || true
grep -q "Build completed successfully" "${OUT}/build.log" || { tail -20 "${OUT}/build.log"; exit 1; }

C=$(docker ps --filter ancestor=gvisor.dev/images/builder --format '{{.Names}}' | head -1)
[[ -n "${C}" ]] || { echo "no build container" >&2; exit 1; }
docker exec "${C}" bash -c "
  set -euo pipefail
  cd '${REPO}'
  B=\$(readlink -f bazel-bin)/test/syscalls/linux
  O=/tmp/macos-sct-runfiles
  rm -rf \$O; mkdir -p \$O/_main
  for t in ${TESTS}; do cp -rLn \$B/\$t.runfiles/_main/. \$O/_main/; done
"

# The guest has 16 KiB pages. Go test helpers that use gVisor's hostarch
# must be built for the same page size.
make build OPTIONS="--//tools/bazeldefs:page_size=16k" TARGETS=//test/cmd/test_app >"${OUT}/build16k.log" 2>&1 || true
grep -q "Build completed successfully" "${OUT}/build16k.log" || { tail -20 "${OUT}/build16k.log"; exit 1; }

docker exec "${C}" bash -c "
  set -euo pipefail
  cd '${REPO}'
  O=/tmp/macos-sct-runfiles
  A=\$(grep -l 'non-16K host' \$(readlink -f bazel-bin)/test/cmd/test_app/test_app_/test_app)
  mkdir -p \$O/_main/test/cmd/test_app/test_app_
  cp -f \$A \$O/_main/test/cmd/test_app/test_app_/test_app
  tar -C \$O -cf /tmp/macos-sct-runfiles.tar .
"
docker cp "${C}:/tmp/macos-sct-runfiles.tar" "${OUT}/runfiles.tar"

rm -rf "${OUT}/rootfs"
mkdir -m 0755 "${OUT}/rootfs"
# A non-root tar cannot keep the owners; the tests run as root.
docker export "$(docker create --platform linux/arm64 ubuntu:22.04)" | tar -xf - -C "${OUT}/rootfs" 2>/dev/null || true
mkdir -p "${OUT}/rootfs/runfiles"
tar -xf "${OUT}/runfiles.tar" -C "${OUT}/rootfs/runfiles"
echo "${OUT}/rootfs"
