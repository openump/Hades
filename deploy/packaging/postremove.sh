#!/bin/sh
# Keep /etc/hades and /var/lib/hades (site configuration, WAL, credentials):
# removing a package must never destroy plant data. Only reload systemd.
set -e
if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
  systemctl daemon-reload || true
fi
exit 0
