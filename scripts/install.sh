#!/bin/sh
# Install mpcloud (unofficial Mobility Print Cloud Print client for Linux)
# from the latest GitHub release.
#
#   curl -fsSL https://raw.githubusercontent.com/mattsoh/mpcloud/main/scripts/install.sh | sh
#
# Options (pass after `sh -s --` when piping):
#   --cups        also install CUPS and add the printers to it (needs setup first)
#   --user        install to ~/.local/bin instead of /usr/local/bin (no sudo)
#   --version V   install a specific release tag instead of the latest
#   --uninstall   remove mpcloud
set -eu

REPO="mattsoh/mpcloud"
WITH_CUPS=0
USER_INSTALL=0
VERSION=""
UNINSTALL=0

while [ $# -gt 0 ]; do
	case "$1" in
	--cups) WITH_CUPS=1 ;;
	--user) USER_INSTALL=1 ;;
	--version) VERSION="$2"; shift ;;
	--uninstall) UNINSTALL=1 ;;
	-h | --help)
		echo "usage: install.sh [--cups] [--user] [--version vX.Y.Z] [--uninstall]"
		exit 0
		;;
	*) echo "unknown option: $1" >&2; exit 2 ;;
	esac
	shift
done

say() { printf '\033[1m==>\033[0m %s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

[ "$(uname -s)" = Linux ] || die "mpcloud only supports Linux"

if [ "$USER_INSTALL" = 1 ]; then
	BIN_DIR="$HOME/.local/bin"
	SUDO=""
else
	BIN_DIR="/usr/local/bin"
	SUDO=""
	if [ "$(id -u)" != 0 ]; then
		command -v sudo >/dev/null || die "sudo not found; re-run with --user"
		SUDO="sudo"
	fi
fi
APP_DIR="${XDG_DATA_HOME:-$HOME/.local/share}/applications"

if [ "$UNINSTALL" = 1 ]; then
	if command -v lpadmin >/dev/null 2>&1 && [ -x "$BIN_DIR/mpcloud" ]; then
		sudo "$BIN_DIR/mpcloud" uninstall-cups || true
	fi
	$SUDO rm -f "$BIN_DIR/mpcloud"
	if [ -L "$BIN_DIR/printer" ]; then
		$SUDO rm -f "$BIN_DIR/printer"
	fi
	rm -f "$APP_DIR/mpcloud-handler.desktop"
	say "Removed mpcloud. Your settings are in ~/.config/mpcloud (delete it to forget your link)."
	exit 0
fi

case "$(uname -m)" in
x86_64 | amd64) ARCH=amd64 ;;
aarch64 | arm64) ARCH=arm64 ;;
armv7* | armv8l) ARCH=armv7 ;;
*) die "unsupported CPU architecture: $(uname -m)" ;;
esac

if [ -n "${MPCLOUD_BASE_URL:-}" ]; then
	BASE="$MPCLOUD_BASE_URL" # mirrors / testing
elif [ -n "$VERSION" ]; then
	BASE="https://github.com/$REPO/releases/download/$VERSION"
else
	BASE="https://github.com/$REPO/releases/latest/download"
fi
ARCHIVE="mpcloud_linux_$ARCH.tar.gz"

fetch() {
	if command -v curl >/dev/null; then
		curl -fsSL -o "$2" "$1"
	elif command -v wget >/dev/null; then
		wget -qO "$2" "$1"
	else
		die "need curl or wget"
	fi
}

TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

say "Downloading $ARCHIVE"
fetch "$BASE/$ARCHIVE" "$TMP/$ARCHIVE" || die "download failed: $BASE/$ARCHIVE"
fetch "$BASE/checksums.txt" "$TMP/checksums.txt" || die "could not download checksums"
(cd "$TMP" && grep " $ARCHIVE\$" checksums.txt | sha256sum -c - >/dev/null) || die "checksum mismatch"
tar -xzf "$TMP/$ARCHIVE" -C "$TMP"

say "Installing to $BIN_DIR"
$SUDO mkdir -p "$BIN_DIR"
$SUDO install -m 0755 "$TMP/mpcloud" "$BIN_DIR/mpcloud"
if [ ! -e "$BIN_DIR/printer" ] || [ -L "$BIN_DIR/printer" ]; then
	$SUDO ln -sf mpcloud "$BIN_DIR/printer"
fi

# Let browsers hand mobilityprint:// links to `mpcloud setup`.
if [ -f "$TMP/packaging/mpcloud-handler.desktop" ]; then
	mkdir -p "$APP_DIR"
	sed "s|Exec=sh -c 'mpcloud |Exec=sh -c '$BIN_DIR/mpcloud |" \
		"$TMP/packaging/mpcloud-handler.desktop" >"$APP_DIR/mpcloud-handler.desktop"
	if command -v xdg-mime >/dev/null; then
		xdg-mime default mpcloud-handler.desktop x-scheme-handler/mobilityprint 2>/dev/null || true
	fi
	if command -v update-desktop-database >/dev/null; then
		update-desktop-database -q "$APP_DIR" 2>/dev/null || true
	fi
fi

# Refresh the CUPS backend if printers were added to CUPS before.
if [ -x /usr/lib/cups/backend/mpcloud ]; then
	say "Updating the CUPS backend"
	if ! sudo install -m 0755 "$BIN_DIR/mpcloud" /usr/lib/cups/backend/mpcloud; then
		say "Couldn't update it; run: sudo $BIN_DIR/mpcloud install-cups"
	fi
fi

"$BIN_DIR/mpcloud" version

if [ "$WITH_CUPS" = 1 ]; then
	if ! command -v lpadmin >/dev/null 2>&1; then
		if command -v apt-get >/dev/null; then
			say "Installing CUPS"
			sudo apt-get install -y cups cups-filters
		elif command -v dnf >/dev/null; then
			sudo dnf install -y cups cups-filters
		elif command -v pacman >/dev/null; then
			sudo pacman -S --needed --noconfirm cups cups-filters
			sudo systemctl enable --now cups.service
		else
			die "install CUPS with your package manager, then run: sudo $BIN_DIR/mpcloud install-cups"
		fi
	fi
	if [ -f "${XDG_CONFIG_HOME:-$HOME/.config}/mpcloud/config.json" ]; then
		say "Adding printers to CUPS"
		sudo "$BIN_DIR/mpcloud" install-cups
	else
		say "CUPS is ready. After setting up your link (below), run: sudo $BIN_DIR/mpcloud install-cups"
	fi
fi

case ":$PATH:" in
*":$BIN_DIR:"*) ;;
*) say "Note: $BIN_DIR is not on your PATH; add it or run $BIN_DIR/mpcloud" ;;
esac

cat <<EOF

Next steps:
  1. Open your organization's Mobility Print setup page and copy the
     mobilityprint:// link (or click it - mpcloud is registered to handle it):
         mpcloud setup 'mobilityprint://...'
  2. Print interactively (signs you in and remembers you):
         mpcloud
  3. Optional - use lp and normal print dialogs:
         sudo $BIN_DIR/mpcloud install-cups
EOF
