#!/bin/sh
# Hades installer. Verifies the release before anything is extracted:
#   1. the Ed25519 signature over checksums.txt, against the pinned release key;
#   2. the archive's SHA-256 against the now-trusted checksums.txt.
# Any mismatch, a missing signature or an OpenSSL without Ed25519 refuses.
#
#   install.sh --from-archive hades_<v>_<os>_<arch>.tar.gz [--checksums checksums.txt] [--prefix /usr/local]
#   install.sh --version <v>          (download from the GitHub release)
#
# POSIX sh (dash, busybox ash, bash). Needs: openssl (3.x), tar, gzip, sha256sum or shasum.
set -eu

# Pinned Ed25519 release public key (SPKI PEM). Override with HADES_RELEASE_PUBKEY=<file>
# only to verify a test release; production installs use the pinned key.
RELEASE_PUBKEY='-----BEGIN PUBLIC KEY-----
@HADES_RELEASE_PUBLIC_KEY@
-----END PUBLIC KEY-----'

die() { echo "install.sh: $*" >&2; exit 1; }
archive='' checksums='' version='' prefix=/usr/local
while [ $# -gt 0 ]; do
  case "$1" in
    --from-archive) archive=${2:?}; shift 2 ;;
    --checksums) checksums=${2:?}; shift 2 ;;
    --version) version=${2:?}; shift 2 ;;
    --prefix) prefix=${2:?}; shift 2 ;;
    -h|--help) sed -n '2,12p' "$0"; exit 0 ;;
    *) die "unknown argument $1" ;;
  esac
done

case "$(uname -s)" in Linux) os=linux ;; Darwin) os=darwin ;; *) die "unsupported OS $(uname -s)" ;; esac
case "$(uname -m)" in x86_64|amd64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; *) die "unsupported architecture $(uname -m)" ;; esac

command -v openssl >/dev/null 2>&1 || die "openssl is required to verify the release signature (apt-get install openssl / dnf install openssl)"
sha256() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi; }

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM
if [ -n "${HADES_RELEASE_PUBKEY:-}" ]; then cp "$HADES_RELEASE_PUBKEY" "$work/release.pub"; else printf '%s\n' "$RELEASE_PUBKEY" > "$work/release.pub"; fi
# The placeholder is assembled at runtime so the release build, which
# substitutes the key line, can never rewrite this check.
unpinned="@HADES_RELEASE""_PUBLIC_KEY@"
grep -qF "$unpinned" "$work/release.pub" && die "this install.sh has no pinned release key; use the one published with the release"

if [ -z "$archive" ]; then
  [ -n "$version" ] || die "give --from-archive <path> or --version <v>"
  base="https://github.com/openump/Hades/releases/download/v$version"
  name="hades_${version}_${os}_${arch}.tar.gz"
  for f in "$name" checksums.txt checksums.txt.sig; do
    curl -fsSL --proto '=https' --tlsv1.2 -o "$work/$f" "$base/$f" || die "download of $f failed"
  done
  archive="$work/$name"; checksums="$work/checksums.txt"
fi
[ -f "$archive" ] || die "archive $archive not found"
[ -n "$checksums" ] || checksums="$(dirname "$archive")/checksums.txt"
[ -f "$checksums" ] || die "checksums.txt not found next to the archive (use --checksums)"
[ -f "$checksums.sig" ] || die "signature $checksums.sig not found; refusing an unsigned release"

# 1. Signature first: nothing in checksums.txt is trusted until it verifies.
openssl pkeyutl -verify -pubin -inkey "$work/release.pub" -rawin -in "$checksums" -sigfile "$checksums.sig" >/dev/null 2>&1 \
  || die "release signature verification FAILED (or this OpenSSL lacks Ed25519): refusing to install"
# 2. Then the archive digest against the signed list.
name=$(basename "$archive")
want=$(awk -v n="$name" '$2 == n { print $1 }' "$checksums")
[ -n "$want" ] || die "$name is not listed in the signed checksums.txt"
got=$(sha256 "$archive")
[ "$got" = "$want" ] || die "SHA-256 mismatch for $name: refusing to install"
case "$name" in *"_${os}_${arch}.tar.gz") ;; *) die "$name is not a $os/$arch archive" ;; esac

tar -xzf "$archive" -C "$work"
dir="$work/${name%.tar.gz}"
[ -x "$dir/hades" ] || die "archive does not contain hades"
mkdir -p "$prefix/bin" "$prefix/share/doc/hades"
install -m 0755 "$dir/hades" "$prefix/bin/hades"
for link in "$dir"/hades-*; do
  [ -L "$link" ] && ln -sf hades "$prefix/bin/$(basename "$link")"
done
for doc in LICENSE THIRD_PARTY_NOTICES.md sbom.cdx.json; do install -m 0644 "$dir/$doc" "$prefix/share/doc/hades/$doc"; done
echo "installed $("$prefix/bin/hades" version) to $prefix/bin (verified: Ed25519 release signature and SHA-256)"
