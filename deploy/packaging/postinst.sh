#!/bin/sh
# Hades package post-install: an unprivileged service account and hardened
# state directories. No site unit is enabled or started: a site must be
# provisioned (credentials, /etc/hades/<role>.env) before anything runs.
set -e
getent group hades >/dev/null 2>&1 || groupadd --system hades
getent passwd hades >/dev/null 2>&1 \
  || useradd --system --gid hades --home-dir /var/lib/hades --no-create-home --shell /usr/sbin/nologin hades
install -d -o root -g hades -m 0750 /etc/hades
install -d -o hades -g hades -m 0750 /var/lib/hades /var/log/hades
if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
  systemctl daemon-reload || true
fi
exit 0
