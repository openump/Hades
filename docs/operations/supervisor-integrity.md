# Supervisor artifact integrity (Hades-030)

The supervisor verifies the reviewed binary and referenced file SHA-256 digests
before starting the child. Each successful verification records device, inode,
size, modification time, change time, mode, UID and GID. The live service
definition is still loaded and compared with its reviewed digest on every probe.

For an unchanged binary or referenced file, `/status` checks metadata without
reading its payload. A metadata change causes a full digest verification before
status can succeed. A same-content replacement can pass if ownership and mode
are unchanged and its digest matches the reviewed value. Changed permissions or
ownership fail even if the file content is unchanged. Content tampering, missing
files and symlinks produce HTTP 409; `hades-service status` reports the service as
unhealthy. A changed file cannot establish a new trusted digest.

A separate worker with a 60-second ticker also rehashes all reviewed artifacts even when
metadata appears unchanged. The cache mutex serializes probes and these sweeps;
a probe coinciding with a full sweep may wait for it. A slow sweep does not block the child supervision/shutdown loop. An OS read
already in progress can finish after cancellation; at most one sweep runs.
Ordinary unchanged probes
perform no binary/config payload reads. Startup and standalone `Validate` always
perform full verification; there is no global or cross-process cache.

Any observed artifact integrity failure stays unhealthy until supervisor restart
with a valid reviewed definition and artifacts. The running child is not silently
replaced or killed by an integrity failure. Operators must reconcile the failed
revision before restarting it. Definition changes retain their existing per-probe
comparison behavior.

Artifact paths must be absolute regular files, at most 512 MiB each, owned by
root or the supervisor's effective service user, without group/world write
permission. Hashing rejects final-component symlinks with `O_NOFOLLOW`, compares
metadata before and after reading, and preserves the existing bounded-read policy.
Files should be provisioned in trusted directories. This is filesystem drift
detection, not protection against a privileged attacker controlling the host or
proof of the exact executable pages already mapped into a running process.

Tests count payload-hash calls across 50 unchanged probes and a forced sweep;
exercise in-place/size/permission/symlink/atomic replacement changes for binaries
and referenced files; restore mtime to check ctime-based invalidation; and verify
live Unix-socket status rejection after a referenced-file edit.
