# Release packaging, verified installation and air-gapped bundles (Hades-042)

Hades ships as one multicall binary (`hades`, Hades-041) in standard OS packages,
a verified installer for connected hosts and an offline bundle for air-gapped
factory floors.

## Building a release

```sh
hades release keygen -key /secure/release.key -pub release.pub   # once; keep the key offline
HADES_VERSION=1.0.0 HADES_RELEASE_KEY=/secure/release.key scripts/package-release
```

`scripts/package-release` writes `dist/`:

| Artifact | Contents |
| --- | --- |
| `hades_<v>_linux_<arch>.deb` / `.rpm` (amd64, arm64) | `/usr/bin/hades` and a `hades-<role>` symlink per role; `hades-service@.service` in `/usr/lib/systemd/system`; `/usr/share/hades` contract root (schemas and the reviewed example catalog); `LICENSE`, `THIRD_PARTY_NOTICES.md` and `sbom.cdx.json` in `/usr/share/doc/hades` |
| `hades_<v>_<os>_<arch>.tar.gz` (linux, darwin) | the binary, its symlinks and the notices |
| `hades_<v>_linux_<arch>.sbom.cdx.json` | CycloneDX 1.5 SBOM of the binary (the Hades-037 inventory) |
| `install.sh` | the installer with the release public key pinned |
| `checksums.txt`, `checksums.txt.sig` | SHA-256 of every artifact; raw Ed25519 signature over that list |
| `homebrew/hades.rb` | a formula template with the darwin tarball digests (no tap is published) |

**Reproducible:** CGO-free, `-trimpath`, no VCS or build-ID stamping, every staged
file clamped to `SOURCE_DATE_EPOCH`, a fixed RPM build host and Go-written tarballs
with fixed owners and times. Two builds of the same source give byte-identical
artifacts; CI builds twice and compares. Packages are built with the pinned
`goreleaser/nfpm:v2.48.0` image, so no packaging tool is installed on the host.

**Notices:** `THIRD_PARTY_NOTICES.md` is generated from the binary's embedded module
list: the Go standard library and runtime (statically linked, BSD-3-Clause) and every
linked module, each with its reviewed license and full license text. The SBOM names
the release version as its subject. A linked module without a license text, or without a reviewed license
identifier, fails the build or its test, so a release can never ship an unattributed
dependency.

## Package behaviour

The unit uses `StateDirectory=hades/%i` and `LogsDirectory=hades/%i`, so systemd creates
each instance's directories, owned by `hades`, before the sandbox starts.

The postinstall script creates the `hades` system user and group (no login shell),
`/etc/hades` (`0750 root:hades`) and `/var/lib/hades` and `/var/log/hades`
(`0750 hades:hades`), and runs `systemctl daemon-reload`. **No unit is enabled or
started**: a site must be provisioned first (credentials, `/etc/hades/<role>.env`).
Removing the package keeps `/etc/hades` and `/var/lib/hades`, so plant data and
credentials survive an uninstall or upgrade.

The `.deb` depends only on `passwd`, which is essential on Debian/Ubuntu, so it
installs on minimal and air-gapped hosts; the `.rpm` depends on `shadow-utils`.

## Verifying and installing on a connected host

```sh
sh install.sh --version 1.0.0                     # download from the GitHub release
sh install.sh --from-archive hades_1.0.0_linux_amd64.tar.gz --prefix /usr/local
```

Before anything is extracted, `install.sh`:

1. verifies the Ed25519 signature over `checksums.txt` against the **release key
   pinned in the script**;
2. then verifies the archive's SHA-256 against that signed list.

A missing signature, a signature from any other key, a tampered `checksums.txt` or
a tampered archive refuses with nothing installed. Re-signing a tampered archive's
checksum with another key is refused too, because the key is pinned. The script is
POSIX `sh` (tested under dash) and verifies with **OpenSSL 3** (`openssl pkeyutl`),
which Ubuntu Server, Debian, RHEL/Rocky and macOS provide. A host without OpenSSL,
or with an OpenSSL lacking Ed25519, is refused rather than installed unverified;
note that minimal container images do not include OpenSSL. You can also verify by
hand:

```sh
openssl pkeyutl -verify -pubin -inkey release.pub -rawin -in checksums.txt -sigfile checksums.txt.sig
sha256sum -c checksums.txt
```

## Air-gapped factory bundle

```sh
HADES_VERSION=1.0.0 HADES_RELEASE_KEY=/secure/release.key HADES_RELEASE_PUB=release.pub \
  HADES_ARCH=amd64 scripts/build-airgap-bundle dist/
```

`hades-airgap-<v>-<arch>.tar.zst` holds that architecture's `.deb`, `.rpm`, tarball
and SBOM, the runtime images saved with `docker save` (`openump/hades-mosquitto`,
`openump/hades-victoriametrics`, `openump/hades-history-proxy` and the pinned Alpine
init image), the secure reference Compose file and configuration, and
`install-offline.sh`. Every file is listed in a `checksums.txt` signed with the
release key, and `images.txt` records each image's content ID.

The reference images are built from the pinned Dockerfiles and are not published
to a registry, so they have no registry digest; the bundle pins their **content
IDs** instead, and the offline installer checks them after `docker load`.

On the air-gapped host, with the release public key obtained through your own
trusted channel:

```sh
zstd -d -c hades-airgap-1.0.0-amd64.tar.zst | tar -xf -
sudo sh hades-airgap-1.0.0-amd64/install-offline.sh --pubkey /media/usb/hades-release.pub
```

It makes no network request. It verifies the bundle signature against the key you
pass (the bundled `release.pub` is for reference and is never trusted), the SHA-256
of every file, that no unlisted file is present, and each loaded image's content ID;
It refuses any symlink or special file in the bundle and requires the files present to
be exactly the signed set. Then it installs the package for the host by its exact name
from the signed manifest (`dpkg`, `rpm`, or the static binary, extracted into a private
temporary directory).
`--no-images` skips Docker image loading on hosts without Docker.

## Verified in CI

The `packaging` job builds the release twice and compares the checksums, verifies the
signature with Go and with OpenSSL, and then:

- installs the `.deb` on clean Ubuntu 24.04 and the `.rpm` on clean Rocky 9 and checks
  the user, directories, symlinks, the registered but not enabled unit and removal;
- starts the packaged `hades-service@` unit under real systemd: it runs as `hades`,
  systemd creates its per-instance state and log directories, and a write outside
  them is confined by `ProtectSystem=strict`;
- runs `install.sh --from-archive` on a valid release and on six tampering cases;
- installs the air-gap bundle with `--network none`, checks that a site scaffolded
  offline passes `hades check`, refuses tampering, smuggled symlinks, unlisted files and
  an untrusted key, and loads the images with matching content IDs.

## Scope

Fleet-wide OTA orchestration stays in Zeus-003. The Homebrew formula is a template;
publishing a tap is a separate decision. Release keys are generated and held by the
release owner; CI uses a throwaway key.
