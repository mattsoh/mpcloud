# Mobility Print Cloud Print protocol

These notes describe how the official Windows client
(`mobility-print-client`, build `2024-09-02-1417`) prints through
Mobility Print Cloud Print. They were worked out by inspecting the client,
a Go 1.20 binary whose function names and type information survive in its
`pclntab` and runtime type descriptors, and were then confirmed against a
live server (Mobility Print 1.0.3790). They're here so others can build
interoperable clients.

## Overview

```
 client ──REST (signalling)──> mp.cloud.papercut.com <──── on-site Mobility Print server
   │                                                               ▲
   └────────────── WebRTC data channels (direct, STUN or TURN) ────┘
```

The cloud service only brokers the WebRTC connection. Printer lists, tokens
and jobs flow end-to-end between the client and the organization's server.

## The link

```
mobilityprint://mp.cloud.papercut.com?token=<JWT>
```

The JWT (RS256) is the **share token**. Its header and claims include `org`
(organization id) and `srv` (server id), plus `iat`, `exp`, `jti`/`lnk` and
`sub: "tokenCreation"`. Clients parse it without verifying it.

## 1. Signalling (REST)

Base URL: `https://<host>/client/v1`. Every request carries these headers:

| Header | Value |
| --- | --- |
| `Authorization` | `Bearer <share token>` |
| `Content-Type`, `Accept` | `application/json` |
| `User-Agent` | `PaperCutMobilityPrintCloudClientGo/1.0.0` |
| `X-PaperCut-Client-Id` | random letters, `xxxxxxxx-xxxxxxxx` |

| Call | Body / response |
| --- | --- |
| `POST /session` | `{}` → `{"id", "iceConfig": {"servers": [{"urls", "username", "credential"}], "maxChunkSize"}}` |
| `PUT /session/{id}/offer` | `{"iceOffer": base64(json(SessionDescription))}` |
| `GET /session/{id}/answer` | 404 `no answer` until ready, then `{"iceAnswer": base64(json(SessionDescription))}` |
| `POST /session/{id}/candidate` | `{"iceCandidates": [json(ICECandidateInit), …]}` |
| `GET /session/{id}/servercandidates?since=<ms>` | `{"iceCandidates": [json(ICECandidateInit)…], "updated": <unix ms>}` |
| `DELETE /session/{id}` | closes the session |

`SessionDescription` is the standard WebRTC `{"type": "offer", "sdp": "…"}`.
The client is the offerer. It trickles its candidates as they're gathered and
polls the answer and server-candidate endpoints about once a second. The ICE
servers seen in practice are Twilio STUN and TURN.

## 2. Data channels

The client creates seven ordered, reliable data channels before making the
offer:

| Label | Request (text message) | Reply |
| --- | --- | --- |
| `SERVERINFO` | empty string | JSON `{"version", "chromeEncryption", "signInUserPass", "signInWithGoogle", "googleOAuthClientId"}` |
| `TOKEN` | the share token | a **print token** (JWT, as text) |
| `PRINTER` | the print token | JSON array of printers (below) |
| `CAPABILITIES` | not used by the Windows client | |
| `JOBDETAILS` | JSON job details (**binary** message) | text: a remember-me token if requested |
| `JOB` | document bytes, as binary messages of `maxChunkSize` | none |
| `PING` | random digits, sent periodically after use | echo |

### Framing

- A reply is sent as one or more **binary** messages followed by the **text**
  message `FINISH`. Concatenate the binary parts.
- Any binary message may be **gzip-compressed** on its own. Receivers try to
  gunzip each one and fall back to the raw bytes.
- Errors are sent as `ERROR:<message>`, either as a text message or as a
  chunked binary payload. Examples: `invalid printer name in print job: …`,
  `user and password authentication failed`.
- Document chunks on `JOB` aren't terminated. The server knows the length
  from `fileSize` in the job details.

### Printer

```json
{
  "name": "Office Printer",
  "description": "PRINTSERVER01",
  "authMode": "per-printer",
  "capabilities": {
    "mediaSizes": [{"name": "ISO_A4", "customDisplayName": "A4",
                    "widthMicrons": 210000, "heightMicrons": 297000,
                    "isDefault": false, "IsContinuousFeed": false}],
    "resolutions": [{"horizontalDpi": 600, "verticalDpi": 600}],
    "color": ["STANDARD_COLOR", "STANDARD_MONOCHROME"],
    "duplex": ["NO_DUPLEX", "LONG_EDGE", "SHORT_EDGE"]
  }
}
```

`authMode` is one of `per-printer`, `per-job` or `standalone`.

### Job details

```json
{
  "clientVersion": "2024-09-02-1417",
  "printToken": "<print token>",
  "fileSize": 12345,
  "params": {
    "id": "<random>",
    "printerName": "https://localhost:9164/printers/Office%20Printer",
    "documentName": "report.pdf",
    "duplex": "NO_DUPLEX",
    "color": "STANDARD_MONOCHROME",
    "copies": "1",
    "mediaSize": "ISO_A4",
    "mediaWidthMicrons": "210000",
    "mediaHeightMicrons": "297000",
    "pageRange": "",
    "contentType": "application/pdf",
    "credentials": {"username": "…", "password": "…",
                    "provider": "", "userID": "", "token": ""},
    "remember": "true",
    "rememberedToken": ""
  }
}
```

- **`printerName` is a URL.** The server parses it and takes the path after
  `/printers/`. The Windows client sends the IPP URL of its local proxy. A
  bare name is rejected with `invalid printer name in print job`.
- Numbers are sent as strings.
- **Authentication:** send `credentials`, or Google `provider`, `userID` and
  `token`, with `remember: "true"`. The `JOBDETAILS` reply is then a
  remember-me JWT. Later jobs send it as `rememberedToken` instead of
  credentials.
- The Windows client sends PostScript from its "PaperCut Global PostScript"
  driver. PDF is accepted too.

## Other notes

- The Windows client also runs a local IPP proxy at `https://localhost:9164`
  (with Windows printer queues pointing at it) and can print straight to an
  on-site server over IPP on port 9163/9164 when it's on the same network.
  mpcloud implements only the cloud path.
- Update metadata: `https://update.mp.cloud.papercut.com/client-version/v3/check-update/pc-mobility-print-client/{windows,macos}`.
  The `linux` channel exists but has no build.
