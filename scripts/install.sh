#!/bin/sh
# Install mpcloud (unofficial Mobility Print Cloud Print client for Linux)
# from the latest GitHub release.
#
#   curl -fsSL https://github.com/mattsoh/mpcloud/releases/latest/download/install.sh | sh
#
# It asks for your organization's link, then adds the printers to your
# system's printing (CUPS, installed if needed) so they show up in every
# app's Print dialog. Options (pass after `sh -s --` when piping):
#   --link URL    use this link (browser address, mobilityprint:// link or token)
#   --no-cups     only install the `mpcloud` command, don't add system printers
#   --user        install to ~/.local/bin instead of /usr/local/bin (no sudo)
#   --version V   install a specific release tag instead of the latest
#   --uninstall   remove mpcloud
set -eu

REPO="mattsoh/mpcloud"
WITH_CUPS=1
LINK=""
USER_INSTALL=0
VERSION=""
UNINSTALL=0

while [ $# -gt 0 ]; do
	case "$1" in
	--cups) WITH_CUPS=1 ;; # the default; kept so old commands still work
	--no-cups) WITH_CUPS=0 ;;
	--link) LINK="$2"; shift ;;
	--user) USER_INSTALL=1 ;;
	--version) VERSION="$2"; shift ;;
	--uninstall) UNINSTALL=1 ;;
	-h | --help)
		echo "usage: install.sh [--link URL] [--no-cups] [--user] [--version vX.Y.Z] [--uninstall]"
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

MP="$BIN_DIR/mpcloud"
"$MP" version
CONFIG="${XDG_CONFIG_HOME:-$HOME/.config}/mpcloud/config.json"

# Prompts read from the terminal: when piped from curl, stdin is this script.
HAVE_TTY=0
if (: </dev/tty) 2>/dev/null; then
	HAVE_TTY=1
fi
ask() {
	printf '%s' "$1" >/dev/tty
	IFS= read -r REPLY </dev/tty || REPLY=""
}

# Set up the Cloud Print link; `mpcloud setup` checks it with the server.
if [ -n "$LINK" ]; then
	"$MP" setup "$LINK" || die "that link was not accepted"
elif [ -f "$CONFIG" ]; then
	say "Already set up (link saved in $CONFIG)"
elif [ "$HAVE_TTY" = 1 ]; then
	echo
	say "Connect to your organization's Mobility Print"
	# mpcloud prints the instructions and checks the link itself, so they
	# always match the version just installed.
	rc=0
	"$MP" setup </dev/tty 2>/dev/null || rc=$?
	if [ "$rc" = 2 ]; then
		# Older mpcloud without a link prompt: ask here instead.
		echo "Copy the address from your browser's address bar (it contains \"?token=\")."
		while :; do
			ask "Paste it here (or press Enter to skip): "
			[ -z "$REPLY" ] && break
			"$MP" setup "$REPLY" && break
			echo "Please try again."
		done
	fi
fi

# Add the printers to the system's printing (CUPS), like the official
# Windows client does, so they appear in every app's Print dialog.
ADDED_PRINTERS=0
if [ "$WITH_CUPS" = 1 ] && [ ! -f "$CONFIG" ]; then
	say "Not connected yet, so no printers were added. Run \`mpcloud\` to connect,"
	say "then add them with: sudo $MP install-cups"
elif [ "$WITH_CUPS" = 1 ]; then
	echo
	say "Adding the printers to your system"
	if ! command -v lpadmin >/dev/null 2>&1; then
		say "Installing CUPS (the standard Linux printing system)"
		if command -v apt-get >/dev/null; then
			sudo apt-get install -y cups cups-filters || true
		elif command -v dnf >/dev/null; then
			sudo dnf install -y cups cups-filters || true
		elif command -v pacman >/dev/null; then
			if sudo pacman -S --needed --noconfirm cups cups-filters; then
				sudo systemctl enable --now cups.service || true
			fi
		fi
	fi
	if ! command -v lpadmin >/dev/null 2>&1; then
		say "Couldn't install CUPS. Install it with your package manager, then run: sudo $MP install-cups"
	elif sudo "$MP" install-cups; then
		ADDED_PRINTERS=1
	else
		say "Couldn't add the printers. Try again later with: sudo $MP install-cups"
	fi
fi

case ":$PATH:" in
*":$BIN_DIR:"*) ;;
*) say "Note: $BIN_DIR is not on your PATH; add it or run $MP" ;;
esac

# With system printers, the Print dialog asks for the PaperCut login the
# first time. Without them, sign in here: PaperCut only confirms a login when
# a job is sent, so this walks through printing something (a test page works).
SIGNED_IN=0
if [ "$ADDED_PRINTERS" = 0 ] && [ "$HAVE_TTY" = 1 ] && [ -f "$CONFIG" ] && ! grep -q '"rememberedToken"' "$CONFIG"; then
	echo
	say "Sign in to PaperCut"
	echo "Printing one document signs you in and saves your login, so printing from"
	echo "apps works without asking again."
	ask "Sign in and print something now? [Y/n] "
	case "$REPLY" in
	[nN]*) ;;
	*)
		echo
		"$MP" </dev/tty || true
		;;
	esac
fi
if [ -f "$CONFIG" ] && grep -q '"rememberedToken"' "$CONFIG"; then
	SIGNED_IN=1
fi

echo
if [ "$ADDED_PRINTERS" = 1 ]; then
	say "All set! Press Ctrl+P in any app and pick one of the printers above."
elif [ "$SIGNED_IN" = 1 ]; then
	say "All set! Print with \`mpcloud\`."
elif [ -f "$CONFIG" ]; then
	say "Connected. Run \`mpcloud\` to sign in and print."
else
	say "Installed. Run \`mpcloud\` to connect and print."
fi
