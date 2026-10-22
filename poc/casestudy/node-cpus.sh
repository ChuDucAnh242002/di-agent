#!/usr/bin/env bash
# node-cpus.sh — split the node's CPUs between the workloads and the agent's own model.
# Runs on the node, as root; provision.sh calls it.
#
# Pods get CPUs 0-5, the six the case-study workloads are sized for; tier1.sh pins the
# decision model to 6-9, and the node's other heavy units (k0s, containerd, Netdata, and the
# agent, which schedule.sh starts with AllowedCPUs=0-5) are held to 0-5 as well, so 6-9 are
# the model's alone and its waiting is never another unit's doing. The split is the kubepods cgroup's cpuset, which every pod's
# cgroup inherits, held by a small unit that re-applies it whenever k0s recreates the
# cgroup. (Kubelet's static CPU manager does not do this: its --reserved-cpus stay in the
# shared pool every non-exclusive pod runs in.) --system-reserved=cpu=4 makes the
# scheduler's allocatable agree. Neither side can take the other's CPUs, so a review never
# shows up as contention and a hog never slows a review.
set -euo pipefail
PODS="${1:-0-5}"
SYSTEM_CPUS="${2:-$(( $(nproc) - 6 ))}" # every CPU past the pods' six is the models'
want="--kubelet-extra-args=--system-reserved=cpu=$SYSTEM_CPUS"
conf=/etc/systemd/system/k0scontroller.service.d/cpus.conf
mkdir -p "$(dirname "$conf")"
if ! grep -qF -- "$want" "$conf" 2>/dev/null; then
    cat >"$conf" <<UNIT
[Service]
ExecStart=
ExecStart=/usr/local/bin/k0s controller --single=true "$want"
UNIT
    systemctl daemon-reload
    systemctl stop k0scontroller
    rm -f /var/lib/k0s/kubelet/cpu_manager_state
    systemctl start k0scontroller
fi
# Cgroups systemd does not manage: kubepods (the kubelet's) and k0s's own containerd.
cat >/usr/local/sbin/kubepods-cpus <<SCRIPT
#!/bin/sh
# Hold the kubelet's and k0s's own cgroups to CPUs $PODS (node-cpus.sh).
grep -qw cpuset /sys/fs/cgroup/cgroup.subtree_control || echo +cpuset >/sys/fs/cgroup/cgroup.subtree_control
grep -qw cpuset /sys/fs/cgroup/system.slice/cgroup.subtree_control || echo +cpuset >/sys/fs/cgroup/system.slice/cgroup.subtree_control
while :; do
    for f in /sys/fs/cgroup/kubepods/cpuset.cpus /sys/fs/cgroup/system.slice/containerd.service/cpuset.cpus; do
        [ -e "\$f" ] && [ "\$(cat \$f)" != "$PODS" ] && echo "$PODS" >"\$f"
    done
    sleep 10
done
SCRIPT
chmod +x /usr/local/sbin/kubepods-cpus
cat >/etc/systemd/system/kubepods-cpus.service <<'UNIT'
[Unit]
Description=Hold the kubepods cgroup to the workloads' CPUs (node-cpus.sh)
After=k0scontroller.service

[Service]
ExecStart=/usr/local/sbin/kubepods-cpus
Restart=always

[Install]
WantedBy=multi-user.target
UNIT
systemctl daemon-reload
systemctl enable --now kubepods-cpus.service >/dev/null
systemctl restart kubepods-cpus.service
for u in k0scontroller.service netdata.service; do
    systemctl set-property "$u" AllowedCPUs="$PODS"
done
for _ in $(seq 60); do k0s kubectl get nodes 2>/dev/null | grep -q " Ready" && break; sleep 5; done
sleep 12
echo "kubepods cpuset $(cat /sys/fs/cgroup/kubepods/cpuset.cpus.effective)"
k0s kubectl get node -o jsonpath='{.items[0].status.allocatable.cpu}{" allocatable CPUs\n"}'
