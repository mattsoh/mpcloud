# mpcloud

Print to **PaperCut Mobility Print Cloud Print** from Linux. This is an unofficial client.

- Use this if you have a Mobility Print
  link (`mobilityprint://mp.cloud.papercut.com?token=...`).
- Your organization's printers show up as normal printers.
  Print with **Ctrl+P** in any app.
- Runs on x86-64, ARM64 and ARMv7. It's a single file with nothing else
  to install. On x86-64 you could also run the official Windows installer
  with [Wine](https://www.winehq.org/).

> [!IMPORTANT]
> This project is not affiliated with, endorsed by or supported by PaperCut
> Software. "PaperCut" and "Mobility Print" are trademarks of their owners.
> mpcloud talks to your organization's print service the same way the
> official client does.

## Install

Run this in a terminal:

```sh
curl -fsSL https://github.com/mattsoh/mpcloud/releases/latest/download/install.sh | sh
```

It will:

1. Ask for your link. Open the link your organization gave you, then copy
   the address from your browser's address bar. It looks like
   `https://mp.cloud.papercut.com/?token=eyJ…`.
2. Install CUPS (Linux's standard printing system) if you don't have it.
3. Add your organization's printers.

That's it. Go to [Printing](#printing).

<details>
<summary>Installer options</summary>

Put options after `sh -s --`, for example:

```sh
curl -fsSL https://github.com/mattsoh/mpcloud/releases/latest/download/install.sh | sh -s -- --link 'https://mp.cloud.papercut.com/?token=…'
```

- `--link URL`: use this link instead of asking for it
- `--no-cups`: only install the `mpcloud` command, don't add printers
- `--user`: install into `~/.local/bin` without sudo
- `--version vX.Y.Z`: install a specific release
- `--uninstall`: remove mpcloud

</details>

<details>
<summary>Other ways to install (.deb, .rpm, from source)</summary>

**Debian / Ubuntu / Fedora:** download the `.deb` or `.rpm` for your
computer from the [latest release](https://github.com/mattsoh/mpcloud/releases/latest), then:

```sh
sudo apt install ./mpcloud_*_arm64.deb   # or: sudo dnf install ./mpcloud_*.rpm
```

**From source** (needs Go 1.24+):

```sh
git clone https://github.com/mattsoh/mpcloud
cd mpcloud
make && sudo make install
```

Then set it up yourself:

```sh
mpcloud setup                     # paste your link when asked
sudo apt install cups cups-filters  # if you don't have CUPS
sudo mpcloud install-cups         # add the printers
```

`mpcloud setup` accepts any of these:

- the browser address: `https://mp.cloud.papercut.com/?token=eyJ…`
- the app link: `mobilityprint://mp.cloud.papercut.com?token=eyJ…`
- just the token: `eyJ…`

</details>

## Printing

**From any app**

- Press **Ctrl+P** and pick one of the printers, for example `Office-Printer`.
- **The first time**, a window asks for your PaperCut username and password.
- After that it remembers you until the login expires.

**From a terminal**

```sh
lp -d Office-Printer document.pdf
lp -d Color-Printer -o sides=two-sided-long-edge -o media=A4 poster.pdf
```

- The first time, the job waits for your PaperCut login. Run
  `mpcloud login` and type your username and password, and the job prints.

**Without CUPS**

- Run `mpcloud`. It asks for the printer, the file (or a test page), sides,
  paper size and copies, and signs you in.
- In scripts: `mpcloud print -p "Printer name" file.pdf`.

**Good to know**

- **Color:** printers with "mono" in their name print black and white.
  Others print in color. To print in gray, pick Grayscale in the print dialog
  or use `-color STANDARD_MONOCHROME`.
- **File types:** PDF always works. Through CUPS, anything an app can print
  works.
- **Nothing came out?** Many organizations hold jobs until you release them
  at the printer or on the PaperCut website.

## Troubleshooting

| You see | What to do |
| --- | --- |
| `user and password authentication failed` | Try your username without `@domain`, or with it. Some organizations only allow Google sign-in, which mpcloud doesn't support yet. `mpcloud info` shows which sign-in methods your organization allows. |
| `the Cloud Print link is invalid or has expired` | Get a new link from your organization, run `mpcloud` and paste it. |
| `timed out waiting for the Mobility Print server to answer` | Your organization's print server is offline. Try again later. |
| A job is stuck "held for authentication" | Your login is missing or expired. Run `mpcloud login`. |
| A job is stuck with "unable to connect to Mobility Print" | Your organization's print server couldn't be reached (mpcloud already tried a few times). Send it again with `lp -i JOB-ID -H resume`, or remove it with `cancel JOB-ID`. `lpstat -o` shows the job IDs. |
| Job sent, but nothing printed | Release it at the printer or on the PaperCut website. |

- **More detail:** add `-v` to any command, for example `mpcloud -v printers`.
- **CUPS logs:** `journalctl -u cups` or `/var/log/cups/error_log`.

## Print from your phone and other devices (Tailscale + AirPrint)

If you use [Tailscale](https://tailscale.com), the computer running mpcloud
can share its printers with your other devices, including iPhones and iPads,
wherever they are.

```sh
sudo mpcloud share     # start sharing
sudo mpcloud unshare   # stop sharing
```

`share` does three things:

- Opens the printers to **your Tailscale devices only** (`100.64.0.0/10`,
  `fd7a:115c:a1e0::/48`). Other people on your Wi-Fi can't print on your
  account.
- Opens port 631 on `tailscale0` if the ufw firewall is on.
- Writes `~/mpcloud-airprint.mobileconfig`, a profile that tells Apple
  devices where the printers are.

Then add the printers on each device:

- **iPhone / iPad:** send the profile with
  `tailscale file cp ~/mpcloud-airprint.mobileconfig my-iphone:`. Open it in
  the Files app, then install it in Settings.
- **Mac:** double-click the profile and install it in System Settings. Or
  add an IP printer: your Tailscale IP, protocol IPP, queue `printers/<name>`.
- **Windows / Linux:** add a printer by URL:
  `http://<tailscale-ip>:631/printers/<name>`.

Keep in mind:

- Jobs go through the computer running mpcloud, so it has to be on.
- Jobs use that computer's PaperCut login.
- Run `mpcloud airprint-profile` to make a new profile if its IP changes.

## All commands

```
mpcloud                               print interactively
mpcloud setup [LINK]                  save your organization's link
mpcloud printers [-json]              list printers and what they support
mpcloud print -p PRINTER [options] FILE|-
    -duplex NO_DUPLEX|LONG_EDGE|SHORT_EDGE
    -color  STANDARD_MONOCHROME|STANDARD_COLOR   (default: from printer name)
    -media  NAME                                 (default: A4, e.g. NA_LETTER)
    -copies N   -pages RANGE   -title TITLE   -type MIME
    -user USER  (password from $MPCLOUD_PASSWORD or prompted)
mpcloud login                         sign in for jobs waiting for your login
mpcloud logout                        forget your saved login (everywhere)
mpcloud info                          show server version and sign-in options
sudo mpcloud install-cups             add the printers to your system
sudo mpcloud uninstall-cups           remove them
sudo mpcloud share / unshare          share printers over Tailscale (AirPrint)
mpcloud airprint-profile              make a new AirPrint profile
```

## Your data

- **Where it's stored:**
  - Before adding printers: `~/.config/mpcloud/config.json` (only you can
    read it).
  - After `sudo mpcloud install-cups`: `/var/lib/mpcloud/config.json`, shared
    by `mpcloud` and the printers. It's owned by `lp` and readable by your
    group.
  - The printer driver goes in `/usr/lib/cups/backend/mpcloud`.
- **What's stored:** your link, username and a "remember me" login.
- **Your password is never saved.** It's sent only to your organization's
  print server, over an encrypted connection.
- Anyone who can read the config file can print as you until the login
  expires. `mpcloud logout` removes the saved login, for both `mpcloud` and
  the printers.

## Uninstall

```sh
sudo mpcloud uninstall-cups        # remove the printers
sudo apt remove mpcloud            # if you installed a package
# or, if you used the quick installer:
curl -fsSL https://github.com/mattsoh/mpcloud/releases/latest/download/install.sh | sh -s -- --uninstall
rm -rf ~/.config/mpcloud           # forget your link and login (after the steps above)
```

## How it works

mpcloud does what the official client does. Neither one talks to printers
directly:

1. The token in your link opens a session at `mp.cloud.papercut.com`.
2. That service sets up a **WebRTC** connection to your organization's own
   Mobility Print server (relayed if needed).
3. Over that connection, mpcloud gets the printer list, then sends the job
   details (including your login) and the document.

mpcloud is written in Go and uses [Pion](https://github.com/pion/webrtc) for
WebRTC. The protocol is documented in [docs/PROTOCOL.md](docs/PROTOCOL.md).

## License

MIT, see [LICENSE](LICENSE).
