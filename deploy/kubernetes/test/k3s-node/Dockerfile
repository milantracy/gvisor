# Test node for gvisor-deploy: Ubuntu with systemd as PID 1 and k3s as a
# systemd service, as on a real host. The build context holds a verified k3s
# binary and k3s.service.
FROM ubuntu:24.04
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
      systemd systemd-sysv dbus ca-certificates iptables kmod && \
    rm -rf /var/lib/apt/lists/* && \
    systemctl mask systemd-logind.service getty.target console-getty.service
COPY k3s /usr/local/bin/k3s
RUN for l in kubectl crictl ctr; do ln -s k3s /usr/local/bin/$l; done
COPY k3s.service /etc/systemd/system/k3s.service
RUN systemctl enable k3s.service
STOPSIGNAL SIGRTMIN+3
ENTRYPOINT ["/sbin/init"]
