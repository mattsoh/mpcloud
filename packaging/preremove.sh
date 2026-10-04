#!/bin/sh
# On full removal, remove CUPS queues created by `mpcloud install-cups`.
case "$1" in
	remove|purge|0)
		if [ -x /usr/bin/mpcloud ] && command -v lpadmin >/dev/null 2>&1; then
			/usr/bin/mpcloud uninstall-cups >/dev/null 2>&1 || true
		fi
		;;
esac
exit 0
