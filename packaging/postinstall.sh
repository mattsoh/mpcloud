#!/bin/sh
# Register the mobilityprint:// link handler.
if command -v update-desktop-database >/dev/null 2>&1; then
	update-desktop-database -q /usr/share/applications || true
fi
# Refresh the CUPS backend if the printers were already added to CUPS.
if [ -x /usr/lib/cups/backend/mpcloud ] && [ -x /usr/bin/mpcloud ]; then
	install -m 0755 /usr/bin/mpcloud /usr/lib/cups/backend/mpcloud || true
fi
exit 0
