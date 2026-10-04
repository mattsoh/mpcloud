# mpcloud

An unofficial Linux client for **PaperCut Mobility Print Cloud Print**.

If you have a `mobilityprint://mp.cloud.papercut.com?token=...` link, this tool
fills that gap. You can print:

- **interactively** with `mpcloud`, which asks for the printer, file, sides,
  paper size and copies
- **from scripts** with `mpcloud print -p "Printer name" file.pdf`
- **from any app** through CUPS, using `lp`, Ctrl+P in GNOME/KDE, LibreOffice
  or Firefox

It runs on x86-64 (you might want to use [Wine](https://www.winehq.org/) with the official installer though),
ARM64, and ARMv7. It's a single static binary with no runtime dependencies (CUPS is
optional).

> [!IMPORTANT]
> This project is not affiliated with, endorsed by or supported by PaperCut
> Software. "PaperCut" and "Mobility Print" are trademarks of their owners.
> mpcloud talks to your organization's print service the same way the
> official client does. Use it only with print services you're authorized to
> use, and follow your organization's IT policies.

## Install

**Quick install:** downloads the latest release, checks its checksum, asks
for your `mobilityprint://` link (and checks that it works), then offers to add
the printers to your system print dialogs:

```sh
curl -fsSL https://raw.githubusercontent.com/mattsoh/mpcloud/main/scripts/install.sh | sh
```

To skip the questions, pass options after `-s --`, for example
`sh -s -- --link 'mobilityprint://...' --cups`. Add `--user` to install into
`~/.local/bin` without sudo.

**Debian / Ubuntu / Fedora packages:** download the `.deb` or `.rpm` for your
architecture from the [latest release](https://github.com/mattsoh/mpcloud/releases/latest), then:

```sh
sudo apt install ./mpcloud_*_arm64.deb   # or: sudo dnf install ./mpcloud_*.rpm
```

**From source** (needs Go 1.24+):

```sh
git clone https://github.com/mattsoh/mpcloud
cd mpcloud
make && sudo make install
```

All methods install `mpcloud` plus a `printer` shortcut to it.

## Setup

If you used the quick installer, you've already done step 1.

1. **Get your link.** On your organization's Mobility Print setup page, choose
   the Cloud Print option. The page or its instructions contain a link like
   `mobilityprint://mp.cloud.papercut.com?token=eyJ…`. You can right-click
   the "open in app" button and copy the link. If mpcloud was installed with
   the installer or a package, clicking the link sets it up automatically.
   Otherwise:

   ```sh
   mpcloud setup 'mobilityprint://mp.cloud.papercut.com?token=…'
   ```

   (Or just run `mpcloud`, which asks for the link the first time.) The link
   is checked with the server before it's saved. If it ever stops working,
   for example because it expired or your organization issued a new one,
   `mpcloud` tells you and asks for a new one.

2. **Print once interactively.** This signs you in:

   ```sh
   mpcloud
   ```

   Pick a printer, sign in with your PaperCut username and password (often
   your network username without `@domain`), choose a file and confirm. The
   server returns a "remember me" token, which mpcloud saves so you don't
   have to sign in again.

3. **Optional: use normal Linux printing.**

   ```sh
   sudo apt install cups cups-filters    # if CUPS isn't installed
   sudo mpcloud install-cups
   ```

   Each Mobility Print queue becomes a CUPS printer, for example
   `Office-Printer` or `Color-Printer`:

   ```sh
   lp -d Office-Printer document.pdf
   lp -d Color-Printer -o sides=two-sided-long-edge -o media=A4 poster.pdf
   ```

   The printers also appear in every app's print dialog. CUPS jobs use your
   saved login, so do step 2 first.

## Usage

```
mpcloud                               interactive mode
mpcloud setup 'mobilityprint://...'   save your organization's link
mpcloud printers [-json]              list printers and their capabilities
mpcloud print -p PRINTER [options] FILE|-
    -duplex NO_DUPLEX|LONG_EDGE|SHORT_EDGE
    -color  STANDARD_MONOCHROME|STANDARD_COLOR   (default: from queue name)
    -media  NAME                                 (default: A4, e.g. NA_LETTER)
    -copies N   -pages RANGE   -title TITLE   -type MIME
    -user USER  (password from $MPCLOUD_PASSWORD or prompted)
mpcloud logout                        forget the saved login
mpcloud info                          show server version and sign-in options
sudo mpcloud install-cups             add printers to CUPS
sudo mpcloud uninstall-cups           remove them
```

Add `-v` before any command for a full protocol log, for example
`mpcloud -v printers`.

PDF works everywhere. Through CUPS, any format CUPS understands (images, text
and office documents via apps) is converted to PostScript first.

**Color** is chosen from the queue name: queues with "mono" in the name print
black and white, and all others print color. Override it with `-color`, or
pick Grayscale in a print dialog.

## How it works

The official client is a Go program. It never talks to printers directly:

1. It exchanges the token from your link for a session at
   `mp.cloud.papercut.com` and uses that service to set up a **WebRTC**
   connection to your organization's on-site Mobility Print server, relayed
   if needed.
2. It talks to that server over WebRTC data channels: it trades the link
   token for a print token, lists printers, then sends job details (including
   your credentials) followed by the document.

mpcloud reimplements that protocol with [Pion](https://github.com/pion/webrtc).
The details are in [docs/PROTOCOL.md](docs/PROTOCOL.md).

## Files and privacy

| Path | Contents |
| --- | --- |
| `~/.config/mpcloud/config.json` | your link, username and remember-me token (mode 0600) |
| `/var/lib/mpcloud/config.json` | copy used by CUPS (owned by `lp`, readable by your group), only after `install-cups` |
| `/usr/lib/cups/backend/mpcloud` | CUPS backend, only after `install-cups` |

Your password is never written to disk. It's sent only to your organization's
Mobility Print server, over the encrypted WebRTC channel. Anyone who can read
the config files can print as you until the token expires. `mpcloud logout`
clears the token.

## Troubleshooting

- **`user and password authentication failed`**: try your username without
  `@domain`, or with it if you left it off. Some organizations allow only
  Google sign-in, which mpcloud doesn't support yet. `mpcloud info` shows
  which sign-in methods your server allows.
- **`the Cloud Print link is invalid or has expired`**: get a fresh link from
  your organization's Mobility Print setup page and run `mpcloud setup '…'`
  (or just `mpcloud`, which asks for it).
- **`timed out waiting for the Mobility Print server to answer`**: your
  organization's server is offline or unreachable. Try again later.
- **CUPS job fails with "login expired"**: run `mpcloud` and print once to
  refresh the saved login.
- **Job sent but nothing prints**: many organizations hold jobs until you
  release them at the printer or in the PaperCut web portal.
- CUPS logs: `journalctl -u cups` or `/var/log/cups/error_log`.

## Uninstall

```sh
sudo mpcloud uninstall-cups        # if you added printers to CUPS
sudo apt remove mpcloud            # package install
# or: curl -fsSL …/scripts/install.sh | sh -s -- --uninstall
rm -rf ~/.config/mpcloud           # forget your link and login
```

## License

MIT, see [LICENSE](LICENSE).
