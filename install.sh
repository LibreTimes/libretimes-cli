#!/bin/sh
# Install lt, the LibreTimes CLI.
#
#   curl -fsSL https://raw.githubusercontent.com/The-LibreTimes/libretimes-cli/main/install.sh | sh
#
# Set LT_VERSION to pin a release, LT_INSTALL_DIR to choose where it lands.
# Checksum verification is mandatory; LT_SKIP_CHECKSUM=1 is the only way past it.
#
# This path exists partly for macOS: curl does not set the com.apple.quarantine
# attribute that a browser download does, so a binary fetched this way runs
# without a Developer ID signature. Downloading the archive from the Releases
# page in a browser instead will be blocked by Gatekeeper — see the README.
#
# POSIX sh, no bashisms: this runs under dash on Debian and Ubuntu.

set -eu

REPO="The-LibreTimes/libretimes-cli"
BINARY="lt"

info() { printf '%s\n' "$*" >&2; }
fail() { printf 'error: %s\n' "$*" >&2; exit 1; }

need() {
	command -v "$1" >/dev/null 2>&1 || fail "$1 is required but not installed"
}

need uname
need tar

# curl or wget, whichever is present.
if command -v curl >/dev/null 2>&1; then
	fetch() { curl -fsSL "$1" -o "$2"; }
	fetch_stdout() { curl -fsSL "$1"; }
elif command -v wget >/dev/null 2>&1; then
	fetch() { wget -qO "$2" "$1"; }
	fetch_stdout() { wget -qO- "$1"; }
else
	fail "neither curl nor wget is installed"
fi

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
	linux)  os=linux ;;
	darwin) os=darwin ;;
	msys*|mingw*|cygwin*)
		fail "on Windows use the .zip from https://github.com/$REPO/releases, or Scoop" ;;
	*) fail "unsupported operating system: $os" ;;
esac

arch=$(uname -m)
case "$arch" in
	x86_64|amd64)  arch=amd64 ;;
	arm64|aarch64) arch=arm64 ;;
	*) fail "unsupported architecture: $arch" ;;
esac

version="${LT_VERSION:-}"
if [ -z "$version" ]; then
	info "Resolving the latest release..."
	# The redirect from /releases/latest carries the tag; reading it avoids a
	# JSON parser and the API rate limit that comes with /repos/.../releases.
	version=$(fetch_stdout "https://api.github.com/repos/$REPO/releases/latest" \
		| sed -n 's/.*"tag_name"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p' \
		| head -n 1)
	[ -n "$version" ] || fail "could not determine the latest version; set LT_VERSION"
fi

bare=${version#v}
archive="${BINARY}_${bare}_${os}_${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/$version"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

info "Downloading $BINARY $version ($os/$arch)..."
fetch "$base/$archive" "$tmp/$archive" \
	|| fail "could not download $archive — check that $version has a $os/$arch build"

# Verify against the release checksums.
#
# Not optional, and that is enforced rather than merely stated: this script
# pipes a remote binary onto the user's PATH, so every path that *cannot*
# verify — no checksums.txt, no line for this archive, no sha256 tool — is
# fatal, not a warning. An install that silently skips verification is
# indistinguishable from one that passed it, which is the whole problem.
#
# LT_SKIP_CHECKSUM=1 is the deliberate override, and it says so loudly.
if [ "${LT_SKIP_CHECKSUM:-}" = 1 ]; then
	info "warning: LT_SKIP_CHECKSUM=1 set — installing $archive WITHOUT verifying it."
else
	fetch "$base/checksums.txt" "$tmp/checksums.txt" 2>/dev/null || fail \
"could not download checksums.txt from $base
  Refusing to install an unverified binary.
  Set LT_SKIP_CHECKSUM=1 to override."

	# Exact field match rather than a regex: the archive name contains dots,
	# which a grep pattern would treat as wildcards. The leading '*' is the
	# binary-mode marker some sha256sum implementations emit.
	expected=$(awk -v want="$archive" \
		'$2 == want || $2 == "*" want { print $1; exit }' "$tmp/checksums.txt")
	[ -n "$expected" ] || fail \
"checksums.txt has no entry for $archive
  Refusing to install an unverified binary.
  Set LT_SKIP_CHECKSUM=1 to override."

	if command -v sha256sum >/dev/null 2>&1; then
		actual=$(sha256sum "$tmp/$archive" | awk '{print $1}')
	elif command -v shasum >/dev/null 2>&1; then
		actual=$(shasum -a 256 "$tmp/$archive" | awk '{print $1}')
	else
		fail "neither sha256sum nor shasum is available, so $archive cannot be verified.
  Refusing to install an unverified binary.
  Install coreutils, or set LT_SKIP_CHECKSUM=1 to override."
	fi

	[ "$actual" = "$expected" ] || fail \
"checksum mismatch for $archive
  expected $expected
  actual   $actual
Do not use this download."

	info "Checksum verified."
fi

tar -xzf "$tmp/$archive" -C "$tmp"
[ -f "$tmp/$BINARY" ] || fail "the archive did not contain a $BINARY binary"
chmod +x "$tmp/$BINARY"

# Prefer a writable directory already on PATH; fall back to ~/.local/bin.
if [ -n "${LT_INSTALL_DIR:-}" ]; then
	dest="$LT_INSTALL_DIR"
elif [ -w /usr/local/bin ] 2>/dev/null; then
	dest=/usr/local/bin
else
	dest="$HOME/.local/bin"
fi

mkdir -p "$dest"
mv "$tmp/$BINARY" "$dest/$BINARY"

info ""
info "Installed $BINARY $version to $dest/$BINARY"

case ":${PATH}:" in
	*":$dest:"*) ;;
	*)
		info ""
		info "$dest is not on your PATH. Add it:"
		info "  export PATH=\"\$PATH:$dest\""
		;;
esac

info ""
info "Next: $BINARY auth login    (or $BINARY auth login --device if this machine has no browser)"
