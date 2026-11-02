#!/bin/sh
# Offline installer for a Hades air-gapped bundle. Makes no network request.
#
#   sh install-offline.sh --pubkey /media/usb/hades-release.pub
#
# 1. Verifies the bundle's checksums.txt Ed25519 signature against a release
#    key YOU already trust (--pubkey); the bundled release.pub is never trusted.
# 2. Verifies the SHA-256 of every bundle file.
# 3. Loads each runtime image and checks its content ID against the signed list.
# 4. Installs the hades package (dpkg/rpm) for this host, or the static binary.
set -eu
die() { echo "install-offline.sh: $*" >&2; exit 1; }
pubkey='' images=1
while [ $# -gt 0 ]; do
  case "$1" in
    --pubkey) pubkey=${2:?}; shift 2 ;;
    --no-images) images=0; shift ;;
    -h|--help) sed -n '2,12p' "$0"; exit 0 ;;
    *) die "unknown argument $1" ;;
  esac
done
here=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
cd "$here"
[ -n "$pubkey" ] || die "--pubkey <release public key you trust> is required; the bundled release.pub is for reference only"
[ -f "$pubkey" ] || die "public key $pubkey not found"
command -v openssl >/dev/null 2>&1 || die "openssl is required to verify the bundle signature"
sha256() { if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1; else shasum -a 256 "$1" | cut -d' ' -f1; fi; }

openssl pkeyutl -verify -pubin -inkey "$pubkey" -rawin -in checksums.txt -sigfile checksums.txt.sig >/dev/null 2>&1 \
  || die "bundle signature verification FAILED: refusing to install"
count=0
while read -r want file; do
  case "$file" in /*|*..*) die "unsafe path $file in checksums.txt" ;; esac
  [ -f "$file" ] || die "$file listed but missing"
  [ "$(sha256 "$file")" = "$want" ] || die "SHA-256 mismatch for $file: refusing to install"
  count=$((count + 1))
done < checksums.txt
# Only regular files and directories may exist: a symlink, device or FIFO could
# redirect an install step outside the verified set.
odd=$(find . ! -type f ! -type d | head -1)
[ -z "$odd" ] || die "non-regular file $odd in bundle: refusing"
# The files present must be exactly the signed set: nothing smuggled in beside it.
listed=$(cut -c67- checksums.txt | LC_ALL=C sort)
present=$(find . -type f ! -name checksums.txt ! -name checksums.txt.sig | sed 's#^\./##' | LC_ALL=C sort)
if [ "$listed" != "$present" ]; then
  extra=$(printf '%s\n' "$present" | grep -vxF "$listed" | head -1)
  die "unlisted file ${extra:-?} in bundle: refusing"
fi
echo "verified: signature and $count files"

if [ "$images" = 1 ]; then
  command -v docker >/dev/null 2>&1 || die "docker is required to load the runtime images (or pass --no-images)"
  while read -r id image file; do
    docker load -q -i "$file" >/dev/null
    [ "$(docker image inspect --format '{{.Id}}' "$image")" = "$id" ] || die "image $image content ID does not match the signed manifest"
    echo "loaded $image ($id)"
  done < images.txt
fi

arch=$(uname -m); case "$arch" in x86_64|amd64) arch=amd64 ;; aarch64|arm64) arch=arm64 ;; esac
# Exact artifact names from the signed manifest, never shell globs.
version=$(sed -n 's/^[0-9a-f]\{64\}  hades_\([0-9.]*\)_linux_[a-z0-9]*\.tar\.gz$/\1/p' checksums.txt | head -1)
[ -n "$version" ] || die "no hades archive listed in the signed checksums.txt"
base="hades_${version}_linux_${arch}"
[ -f "$base.tar.gz" ] || die "this bundle has no $arch artifacts"
if command -v dpkg >/dev/null 2>&1 && [ -f /etc/debian_version ]; then
  dpkg -i "./$base.deb"
elif command -v rpm >/dev/null 2>&1; then
  rpm -i --nodeps "./$base.rpm"
else
  # Extract into a private directory: never a predictable path in /tmp.
  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT INT TERM
  tar -xzf "$base.tar.gz" -C "$tmp"
  install -m 0755 "$tmp/$base/hades" /usr/local/bin/hades
  for link in "$tmp/$base"/hades-*; do [ -L "$link" ] && ln -sf hades "/usr/local/bin/$(basename "$link")"; done
fi
echo "installed $(hades version) offline"
