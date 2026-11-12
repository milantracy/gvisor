# Installer image for gvisor-deploy.
#
# The build context must contain:
#   gvisor/            Contents of a gVisor release tarball (runsc,
#                      containerd-shim-runsc-v1 and gvisor-bin/).
#   gvisor-installer   The installer binary (//deploy/kubernetes/installer).
#
# busybox provides nsenter, which the installer uses to restart containerd
# in the host namespaces.
FROM busybox:1.37
COPY gvisor/ /opt/gvisor-artifacts/
COPY gvisor-installer /usr/local/bin/gvisor-installer
ENTRYPOINT ["/usr/local/bin/gvisor-installer"]
CMD ["run"]
