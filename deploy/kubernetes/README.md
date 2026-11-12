# gvisor-deploy (prototype)

Installs gVisor on the nodes of a Kubernetes cluster and creates a `runsc`
RuntimeClass.

## Quick start

From the repository root, with a gVisor release tarball (`gvisor.tar.zstd`):

```shell
# Build the installer image.
mkdir -p ctx/gvisor
tar --zstd -xf gvisor.tar.zstd -C ctx/gvisor
CGO_ENABLED=0 go build -o ctx/gvisor-installer ./deploy/kubernetes/installer
docker build -t <registry>/gvisor-installer:dev -f deploy/kubernetes/Dockerfile ctx
docker push <registry>/gvisor-installer:dev

# Install.
helm install gvisor deploy/kubernetes/chart/gvisor-deploy -n kube-system --wait \
  --set image.repository=<registry>/gvisor-installer --set image.tag=dev

# Run a command in gVisor.
kubectl run box --image=busybox --overrides='{"spec":{"runtimeClassName":"runsc"}}' -- sleep infinity
kubectl wait --for=condition=Ready pod/box
kubectl exec box -- dmesg

# Uninstall. Delete gVisor pods first.
kubectl delete pod box
helm uninstall gvisor -n kube-system --wait
```

On kind, skip `docker push`. Instead, run
`kind load docker-image <registry>/gvisor-installer:dev`, and add
`--set image.pullPolicy=Never` to `helm install`.

## Supported nodes

| Cluster                                    | Tested with                                       |
| ------------------------------------------ | ------------------------------------------------- |
| Kubernetes with containerd (kubeadm, kind) | Kubernetes 1.31 and 1.37; containerd 1.7.18, 2.3.4 |
| k3s (server node)                          | k3s v1.37.2+k3s1                                  |

Kubernetes 1.30 or later is needed for the node-label admission policy.

On other nodes, the installer stops with an error and does not change the
node.

## How it works

A privileged DaemonSet runs the installer on each node. The installer:

1.  Copies the release files to `/opt/gvisor/versions/<hash>/`.
2.  Writes `/etc/gvisor/runsc.toml` from the `runscFlags` chart value.
3.  Adds the `runsc` handler to the containerd config, between
    `# BEGIN gvisor-deploy` and `# END gvisor-deploy` lines. On k3s, it
    edits the containerd config template instead.
4.  Restarts containerd (or k3s) if the config changed, and checks that the
    CRI plugin loaded. If it did not, it restores the previous config.
5.  Labels the node `gvisor.dev/runsc=installed`. The RuntimeClass schedules
    gVisor pods only on labeled nodes.

`helm upgrade` installs new binaries next to the old ones. Running containers
continue to use the old binaries. A new container in a pod that started
before the upgrade uses the new binaries.

`helm uninstall` removes everything that the installer added, and restarts
containerd.

## Testing

`test/e2e.sh` tests install, upgrade and uninstall on a kind cluster, and
checks that the nodes are unchanged after uninstall:

```shell
./deploy/kubernetes/test/e2e.sh <release-dir> [<upgrade-release-dir>]
```

For k3s, put a k3s binary in `test/k3s-node/`, then:

```shell
docker build -t gd-k3s-node:latest deploy/kubernetes/test/k3s-node
CLUSTER_TYPE=k3s ./deploy/kubernetes/test/e2e.sh <release-dir> [<upgrade-release-dir>]
```
