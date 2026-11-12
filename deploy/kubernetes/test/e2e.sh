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

# End-to-end test of gvisor-deploy on a kind or k3s cluster:
# install -> run a gVisor pod -> upgrade -> uninstall -> check the nodes are
# back to their original state.
#
# Usage: [NODE_IMAGE=kindest/node:...] e2e.sh <release-dir> [<upgrade-release-dir>]
#
# Each release dir holds an extracted gVisor release tarball (runsc,
# containerd-shim-runsc-v1, gvisor-bin/). If <upgrade-release-dir> is given,
# the upgrade step also switches to these binaries. Requires docker, kind,
# kubectl, helm and go.
#
# With CLUSTER_TYPE=k3s, the test runs on a single k3s node in a container,
# from the image built from k3s-node/ (K3S_NODE_IMAGE, default
# gd-k3s-node:latest).
#
# With CONTAINERD_CONFIG_MIGRATE=1, the test first migrates the containerd
# config of the nodes to the latest version (requires containerd 2.x).

set -euo pipefail

RELEASE_DIR="$(realpath "${1:?usage: $0 <release-dir> [<upgrade-release-dir>]}")"
UPGRADE_DIR="${2:+$(realpath "$2")}"
NODE_IMAGE="${NODE_IMAGE:-}"
CLUSTER="${CLUSTER:-gvisor-deploy-e2e}"
CLUSTER_TYPE="${CLUSTER_TYPE:-kind}"
K3S_NODE_IMAGE="${K3S_NODE_IMAGE:-gd-k3s-node:latest}"
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEPLOY_DIR="$(dirname "${HERE}")"
REPO_ROOT="$(cd "${DEPLOY_DIR}/../.." && pwd)"
CHART="${DEPLOY_DIR}/chart/gvisor-deploy"
NS=kube-system
WORK="$(mktemp -d)"
cleanup() {
  rm -rf "${WORK}"
  if [[ -z "${KEEP_CLUSTER:-}" ]]; then
    if [[ "${CLUSTER_TYPE}" == k3s ]]; then
      docker rm -f -v "${CLUSTER}" >/dev/null 2>&1 || true
    else
      kind delete cluster --name "${CLUSTER}" >/dev/null 2>&1 || true
    fi
  fi
}
trap cleanup EXIT

log() { echo "=== $*"; }
fail() { echo "FAIL: $*" >&2; exit 1; }

build_image() {
  local tag="$1" release="$2"
  log "Building installer image gvisor-installer:${CLUSTER}-${tag} with $("${release}/runsc" --version | head -1)"
  rm -rf "${WORK}/ctx"
  mkdir -p "${WORK}/ctx"
  cp -r "${release}" "${WORK}/ctx/gvisor"
  (cd "${REPO_ROOT}" && CGO_ENABLED=0 go build -o "${WORK}/ctx/gvisor-installer" ./deploy/kubernetes/installer)
  docker build -q -t "gvisor-installer:${CLUSTER}-${tag}" -f "${DEPLOY_DIR}/Dockerfile" "${WORK}/ctx" >/dev/null
}
build_image e2e "${RELEASE_DIR}"
UPGRADE_TAG=e2e
if [[ -n "${UPGRADE_DIR}" ]]; then
  build_image e2e-upgrade "${UPGRADE_DIR}"
  UPGRADE_TAG=e2e-upgrade
fi

export KUBECONFIG="${WORK}/kubeconfig"
IMAGES=("gvisor-installer:${CLUSTER}-e2e" "gvisor-installer:${CLUSTER}-${UPGRADE_TAG}")
if [[ "${CLUSTER_TYPE}" == k3s ]]; then
  log "Creating k3s node ${CLUSTER}"
  docker rm -f -v "${CLUSTER}" >/dev/null 2>&1 || true
  docker run -d --name "${CLUSTER}" --hostname "${CLUSTER}" --privileged --cgroupns=private \
    --tmpfs /run --tmpfs /run/lock -v /var/lib/rancher -v /var/lib/kubelet "${K3S_NODE_IMAGE}" >/dev/null
  for _ in $(seq 90); do
    docker exec "${CLUSTER}" kubectl get node "${CLUSTER}" 2>/dev/null | grep -q " Ready" && break
    sleep 2
  done
  docker exec "${CLUSTER}" kubectl wait --for=condition=Ready "node/${CLUSTER}" --timeout=60s >/dev/null
  NODE_IP="$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "${CLUSTER}")"
  docker exec "${CLUSTER}" cat /etc/rancher/k3s/k3s.yaml | sed "s/127.0.0.1/${NODE_IP}/" >"${KUBECONFIG}"
  for img in "${IMAGES[@]}"; do
    docker save "${img}" | docker exec -i "${CLUSTER}" ctr --address /run/k3s/containerd/containerd.sock -n k8s.io images import - >/dev/null
  done
  NODES="${CLUSTER}"
  CONTAINERD_DIR=/var/lib/rancher/k3s/agent/etc/containerd
  # Wait for the system pods, so that their start does not interfere with
  # the k3s restart.
  kubectl -n kube-system wait --for=condition=Ready pods --all --timeout=180s >/dev/null || true
else
  log "Creating kind cluster ${CLUSTER} (2 nodes)"
  kind delete cluster --name "${CLUSTER}" >/dev/null 2>&1 || true
  cat >"${WORK}/kind.yaml" <<EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
- role: worker
EOF
  kind create cluster --name "${CLUSTER}" --config "${WORK}/kind.yaml" ${NODE_IMAGE:+--image "${NODE_IMAGE}"} --wait 120s
  kind export kubeconfig --name "${CLUSTER}" --kubeconfig "${KUBECONFIG}"
  kind load docker-image "${IMAGES[@]}" --name "${CLUSTER}"
  NODES=$(kind get nodes --name "${CLUSTER}")
  CONTAINERD_DIR=/etc/containerd
fi

if [[ -n "${CONTAINERD_CONFIG_MIGRATE:-}" ]]; then
  log "Migrating containerd config to the latest version"
  for n in ${NODES}; do
    docker exec "${n}" sh -c 'containerd config migrate > /tmp/config.toml && mv /tmp/config.toml /etc/containerd/config.toml && systemctl restart containerd'
    docker exec "${n}" grep -qE '^version = [34]' /etc/containerd/config.toml || fail "config on ${n} was not migrated"
    docker exec "${n}" grep '^version' /etc/containerd/config.toml
  done
  kubectl wait --for=condition=Ready nodes --all --timeout=120s >/dev/null
fi

# On kind, the control-plane node already has a runsc handler (as after a
# manual gVisor install). The installer must refuse it without changes, and
# must not block the install or the uninstall of the other nodes.
INSTALLED_NODES="${NODES}"
REFUSED_NODE=""
if [[ "${CLUSTER_TYPE}" == kind ]]; then
  REFUSED_NODE="${CLUSTER}-control-plane"
  INSTALLED_NODES="$(grep -vx "${REFUSED_NODE}" <<<"${NODES}")"
  log "Adding a runsc handler to ${REFUSED_NODE}"
  docker exec "${REFUSED_NODE}" sh -c '
    p=io.containerd.grpc.v1.cri
    grep -qE "^version = [34]" /etc/containerd/config.toml && p=io.containerd.cri.v1.runtime
    printf "\n[plugins.\"%s\".containerd.runtimes.runsc]\n  runtime_type = \"io.containerd.runsc.v1\"\n" "$p" >>/etc/containerd/config.toml'
fi

snapshot() {
  for n in ${NODES}; do
    echo "## ${n}"
    docker exec "${n}" sh -c "cat ${CONTAINERD_DIR}/config.toml; ls -1A ${CONTAINERD_DIR} /opt/gvisor /etc/gvisor 2>&1 | sort"
  done
}
snapshot >"${WORK}/before.txt"
for n in ${NODES}; do
  if [[ "${CLUSTER_TYPE}" == k3s ]]; then
    docker exec "${n}" k3s --version | head -1
  else
    docker exec "${n}" containerd --version
  fi
done

# Restarting k3s also restarts its kubelet, so log requests can fail for a
# few seconds after an install.
installer_logs() {
  for _ in $(seq 30); do
    kubectl -n "${NS}" logs -l app.kubernetes.io/instance=gvisor --tail=-1 --prefix 2>/dev/null && return
    sleep 2
  done
  kubectl -n "${NS}" logs -l app.kubernetes.io/instance=gvisor --tail=-1 --prefix
}

log "helm install"
helm install gvisor "${CHART}" -n "${NS}" \
  --set image.repository=gvisor-installer --set image.tag="${CLUSTER}-e2e" --set image.pullPolicy=Never \
  --set runscFlags.platform=systrap --wait --timeout 5m
# helm --wait accepts a DaemonSet with up to maxUnavailable pods not ready.
kubectl -n "${NS}" rollout status daemonset/gvisor-installer --timeout 5m
installer_logs

for n in ${INSTALLED_NODES}; do
  [[ "$(kubectl get node "${n}" -o jsonpath='{.metadata.labels.gvisor\.dev/runsc}')" == "installed" ]] || fail "node ${n} not labeled"
done
if [[ -n "${REFUSED_NODE}" ]]; then
  [[ -z "$(kubectl get node "${REFUSED_NODE}" -o jsonpath='{.metadata.labels.gvisor\.dev/runsc}')" ]] ||
    fail "refused node ${REFUSED_NODE} is labeled"
  grep -q "NOT INSTALLING gVisor on node \"${REFUSED_NODE}\"" <<<"$(installer_logs)" ||
    fail "installer did not refuse ${REFUSED_NODE}"
  docker exec "${REFUSED_NODE}" test ! -e /opt/gvisor || fail "refused node ${REFUSED_NODE} was changed"
  echo "installer refused ${REFUSED_NODE} without changes"
fi

run_pod() {
  local name="$1"
  kubectl delete pod "${name}" --ignore-not-found >/dev/null
  cat <<EOF | kubectl apply -f - >/dev/null
apiVersion: v1
kind: Pod
metadata:
  name: ${name}
spec:
  runtimeClassName: runsc
  restartPolicy: Never
  containers:
  - name: c
    image: busybox:1.37
    command: ["dmesg"]
EOF
  kubectl wait --for=jsonpath='{.status.phase}'=Succeeded "pod/${name}" --timeout=120s >/dev/null ||
    { kubectl describe pod "${name}"; fail "pod ${name} did not succeed"; }
  grep -q "Starting gVisor" <<<"$(kubectl logs "${name}")" || fail "pod ${name} did not run in gVisor"
  echo "pod ${name} ran in gVisor on node $(kubectl get pod "${name}" -o jsonpath='{.spec.nodeName}')"
}

log "Checking installer permissions"
SA="system:serviceaccount:${NS}:gvisor-installer"
[[ "$(kubectl auth can-i patch daemonsets -n "${NS}" --as="${SA}" || true)" == "no" ]] ||
  fail "installer ServiceAccount can patch DaemonSets"
read -r NODE_A NODE_B <<<"$(echo ${INSTALLED_NODES} ${REFUSED_NODE})"
# Use a token bound to the installer pod on NODE_A, like the one mounted in
# that pod: it carries the node name.
POD_A="$(kubectl -n "${NS}" get pods -l app.kubernetes.io/instance=gvisor --field-selector spec.nodeName="${NODE_A}" -o name | cut -d/ -f2)"
TOKEN_A="$(kubectl -n "${NS}" create token gvisor-installer --bound-object-kind Pod --bound-object-name "${POD_A}")"
SERVER="$(kubectl config view --minify -o jsonpath='{.clusters[0].cluster.server}')"
as_node() {
  kubectl --kubeconfig=/dev/null --server="${SERVER}" --insecure-skip-tls-verify --token="${TOKEN_A}" "$@"
}
as_node label node "${NODE_A}" gvisor.dev/runsc=installed --overwrite >/dev/null ||
  fail "installer cannot set its own node label"
if [[ -n "${NODE_B}" ]]; then
  as_node label node "${NODE_B}" gvisor.dev/runsc- >/dev/null 2>&1 &&
    fail "installer can change another node"
fi
as_node label node "${NODE_A}" e2e-other=x >/dev/null 2>&1 &&
  fail "installer can set other labels"
as_node taint node "${NODE_A}" e2e=x:NoSchedule >/dev/null 2>&1 &&
  fail "installer can taint its node"
echo "installer can only set gvisor.dev/runsc on its own node"

log "Running gVisor pods"
run_pod gvisor-test-1

# A long-running gVisor pod that lives across the upgrade.
cat <<EOF | kubectl apply -f - >/dev/null
apiVersion: v1
kind: Pod
metadata:
  name: gvisor-sleeper
spec:
  runtimeClassName: runsc
  containers:
  - name: c
    image: busybox:1.37
    command: ["sleep", "3600"]
EOF
kubectl wait --for=condition=Ready pod/gvisor-sleeper --timeout=120s >/dev/null

SLEEPER_NODE="$(kubectl get pod gvisor-sleeper -o jsonpath='{.spec.nodeName}')"
OLD_BINARY="$(docker exec "${SLEEPER_NODE}" sed -n 's/^binary_name = "\(.*\)"$/\1/p' /etc/gvisor/runsc.toml)"

upgrade() {
  helm upgrade gvisor "${CHART}" -n "${NS}" --reuse-values "$@" --wait --timeout 5m
  # helm --wait accepts a DaemonSet with up to maxUnavailable pods not updated.
  kubectl -n "${NS}" rollout status daemonset/gvisor-installer --timeout 5m
  upgrade_logs="$(installer_logs)"
  echo "${upgrade_logs}"
  [[ "$(grep -c "not restarting containerd" <<<"${upgrade_logs}")" == "$(wc -w <<<"${INSTALLED_NODES}")" ]] ||
    fail "upgrade with unchanged containerd config restarted containerd"
}

# A flags-only upgrade reuses the installed version directory. It must not
# rewrite its files: the in-use check below relies on their inodes.
log "helm upgrade (runsc flags only)"
upgrade --set runscFlags.debug=true
grep -q "binaries already installed" <<<"${upgrade_logs}" || fail "flags-only upgrade reinstalled the binaries"
for n in ${INSTALLED_NODES}; do
  docker exec "${n}" grep -q 'debug = "true"' /etc/gvisor/runsc.toml || fail "runsc.toml on ${n} not updated"
done
run_pod gvisor-test-2

if [[ -n "${UPGRADE_DIR}" ]]; then
  log "helm upgrade (image ${UPGRADE_TAG})"
  upgrade --set image.tag="${CLUSTER}-${UPGRADE_TAG}"
  NEW_BINARY="$(docker exec "${SLEEPER_NODE}" sed -n 's/^binary_name = "\(.*\)"$/\1/p' /etc/gvisor/runsc.toml)"
  [[ "${NEW_BINARY}" != "${OLD_BINARY}" ]] || fail "runsc.toml still points to ${OLD_BINARY}"
  docker exec "${SLEEPER_NODE}" test -x "${OLD_BINARY}" || fail "in-use ${OLD_BINARY} was removed"
  echo "new pods use ${NEW_BINARY}; in-use ${OLD_BINARY} is kept"
  docker exec "${SLEEPER_NODE}" "${NEW_BINARY}" --version | head -1
  run_pod gvisor-test-3
fi
kubectl exec gvisor-sleeper -- true || fail "pod started before the upgrades no longer works"
echo "pod started before the upgrades still works"

log "helm uninstall"
kubectl delete pod gvisor-test-1 gvisor-test-2 gvisor-test-3 gvisor-sleeper --ignore-not-found --wait >/dev/null
helm uninstall gvisor -n "${NS}" --wait --timeout 5m
snapshot >"${WORK}/after.txt"
if ! diff -u "${WORK}/before.txt" "${WORK}/after.txt"; then
  fail "nodes are not back to their original state"
fi
for n in ${NODES}; do
  [[ -z "$(kubectl get node "${n}" -o jsonpath='{.metadata.labels.gvisor\.dev/runsc}')" ]] || fail "node ${n} still labeled"
done
kubectl get runtimeclass runsc >/dev/null 2>&1 && fail "RuntimeClass still present"

log "PASS"
