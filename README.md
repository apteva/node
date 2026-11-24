# Apteva Node

A small device (Raspberry Pi + touchscreen) that gives an Apteva agent a
physical body: a face you can see, a place to talk to it, and hardware it
can use — relays, LEDs, buttons, sensors, a camera — with a person on the
device approving anything sensitive.

```
Pi                                             Apteva instance
┌──────────────────────────────┐   outbound    ┌──────────────────────┐
│ device UI (localhost only)   │   WebSocket   │ /api/devices/link    │
│        ▲                     │  ───────────► │   (apteva-server     │
│ apteva-node daemon           │               │    devices_*.go;     │
│   capabilities · guard ·     │               │    node-sim stands   │
│   link · offline queue       │               │    in for dev)       │
└──────────────────────────────┘               └──────────────────────┘
```

The device UI is **not** the Apteva dashboard. It talks only to the local
daemon, and the daemon talks only to the server over the link with a
device credential. The agent is managed in the dashboard; the Node is where
it lives physically.

## Run it on a laptop

```sh
GOWORK=off go run ./cmd/node-sim                       # stand-in server on :7490
bun run build:ui
GOWORK=off go run ./cmd/apteva-node --sim --config node.example.yaml --ui-dir ui/dist
open http://127.0.0.1:7480
```

`node-sim` plays a scripted agent: try "take a photo", "what's the
temperature?", "turn the lamp on". It also takes commands on stdin or via
`curl -d '<cmd>' http://127.0.0.1:7490/sim`: `tools`, `call <tool> [json]`,
`say <text>`, `status <state> [detail]`, `kick`, `approve [code]`,
`deny [code]`, `revoke`.

### Setup and the device preview

```sh
GOWORK=off go run ./cmd/node-sim
bun run build:ui && GOWORK=off go run ./cmd/apteva-node --sim --config node.dev.yaml --ui-dir ui/dist
bun ui/preview/serve.ts                                # → http://127.0.0.1:7470
```

`node.dev.yaml` has no server, so the device boots into first-boot setup:
network → Apteva address → pairing code → hardware check → PIN. Every step
can be skipped; skipping Apteva leaves a **standalone** device (hardware
screen under a big clock, no agent, nothing queued, no safe-state timer).
Approve the code at `http://127.0.0.1:7490/pair`, or with `approve` on
node-sim. `revoke` plays "removed from the dashboard": the device drops to
standalone and says why. To reset, use Settings → "Hold to reset device", or
"Forgot PIN?" on the lock screen (a reset also unpairs, so it can't be used
to take a device over); `apteva-node --reset` does the same from a shell.

In the preview, **Sign in** pairs with node-sim and approves it in one step
(the signed-in path, nine sample agents); **First boot** resets to setup.

The preview page shows the UI at real panel sizes (Pi Touch Display 2,
HyperPixel 4, 3.5" SPI, …) with node-sim controls beside it.

Pairing and the PIN live in `state_dir` (`/var/lib/apteva-node/state.json`,
0600). A `server.url` + token in node.yaml pre-provisions the device and
skips setup.

## Put it on a Pi

```sh
./build.sh pi            # → dist/apteva-node-<version>-linux-arm64.tar.gz
# copy to the Pi, untar, then:
sudo sh install.sh
```

Installs two systemd units: `apteva-node` (the daemon) and
`apteva-node-screen` (cog/WPE kiosk on the display, no desktop needed).

## node.yaml

The owner declares every piece of hardware; nothing else is reachable. See
[node.example.yaml](node.example.yaml).

| kind | tools for the agent | events |
|---|---|---|
| `relay`, `led` | `<id>_set {on}`, `<id>_get` | — |
| `button` | `<id>_get` | `pressed` |
| `i2c_sensor` (`bme280`) | `<id>_read` | `<field>_above` / `<field>_below` thresholds |
| `camera` | `<id>_capture {width?, height?}` (base64 JPEG) | — |
| built-in | `system_info` | — |

### Cameras

- `index` picks the camera port (as `rpicam-hello --list-cameras` numbers
  it); `rotation` (0 or 180), `hflip` and `vflip` correct for how it is
  mounted. Photos are taken with `rpicam-still` (`libcamera-still` on older
  Pi OS).
- `width`/`height` are the default size. An agent may ask for another size
  up to `max_width`/`max_height` (default: the default size); bigger
  requests are scaled down, and the result reports the size actually sent.
- A camera **asks on screen unless `confirm: none` is written** for it.
- One capture runs at a time; the camera can only be opened once, so other
  requests wait their turn.
- A photo must fit in one link frame (the server reads at most 1 MiB): over
  720 KiB the daemon re-encodes it at lower quality, then lower resolution,
  or fails with "photo too large".
- At start the daemon checks that each declared camera exists and shows
  "no camera found on port N" (or a missing `rpicam-apps`) on the hardware
  screen and in first-boot setup.
- Every photo that leaves the device is shown on screen for a few seconds
  ("Photo sent … to <agent>") and noted in the conversation.

### Safety, enforced on the device

- The device has no physical controls; every decision is on the screen.
  `confirm: screen` — the person taps Allow. `confirm: hold` — Allow must be
  pressed and held (1.2 s), so a stray touch never opens a door; a tap can
  still deny. Unanswered requests are denied after `confirm_timeout`.
- After the link is down for `safe_state_after`, outputs with a
  `safe_state` are driven to it. Outputs also start inactive at boot.
- The local UI listens on loopback and rejects foreign `Host`/`Origin`
  headers.

## App widgets

The Presence screen hosts app widgets: the same
`apteva-native-surface/v1` documents (`presentation: "widget"`) that apps
already ship for native dashboard widgets — see
[app-sdk/NATIVE_SURFACES.md](../app-sdk/NATIVE_SURFACES.md). No app code runs
on the device; the document is data, and [ui/src/surfaces.ts](ui/src/surfaces.ts)
draws all nine block types (`metric`, `metrics`, `text`, `status`,
`progress`, `list`, `empty_state`, `divider`, `action`).

- Data and action requests go UI → daemon → link (`app_request` /
  `app_response`) → server → app. The device never holds app credentials and
  can only reach apps that have a widget on it.
- `app_event` frames bump a widget when one of its `refresh_topics` fires.
- Reads are cached in the daemon; offline tiles show the last answer, marked.
- Actions: `request`, `confirm` and `form` (text, number, boolean, choice
  inputs) work on the device; `open_url`, `copy_result`, `share_result`,
  `file_upload` and file/resource inputs show as "not on this device".
- App sheets are labelled with the app's name and sit under the device's
  own approval sheet, so an app can't draw a hardware approval.

`node-sim` places two widgets: tasks' real `task-overview.json` (copied
verbatim) and `store-today.json`, which uses every block type. Both pass
app-sdk's `ParseNativeSurface`.

## Link protocol (v1)

One WebSocket to `<server>/api/devices/link`, `Authorization: Bearer
<device token>`. Frames are `{"type", "id"?, "data"?}` — see
[internal/proto/frames.go](internal/proto/frames.go).

| Node → server | Server → Node |
|---|---|
| `hello` (name, version, tools, events, renders) | `welcome` (bound agent id, name, icon) |
| `result` (for an `invoke` id) | `invoke` (tool, args) |
| `event`, `message`, `state` — queued while offline | `say`, `status` |
| `app_request` | `app_response`, `widgets`, `app_event` |
| `select_agent` (agent id) | `agents` (the project's agents and their status) |

The Agents tab shows `agents` as a 3×3 grid; picking one sends
`select_agent`, and the server answers with a new `welcome`.

## Layout

| Path | What |
|---|---|
| `cmd/apteva-node` | daemon |
| `cmd/node-sim` | stand-in server + scripted agent |
| `internal/config` | node.yaml parsing and validation |
| `internal/hw` | backends: Linux (GPIO chardev v2, I2C, rpicam) and sim; BME280 driver |
| `internal/guard` | on-device approvals |
| `internal/link` | WebSocket client, reconnect, offline queue |
| `internal/node` | core: capabilities → tools, events, safe state, UI snapshot |
| `internal/localui` | serves the UI + its state socket |
| `ui/` | device UI (TypeScript, no framework), embedded into the binary |

The server side lives in apteva-server (`devices*.go`: pairing, the link
hub, the agent tool bridge, app requests). Not built yet: native-surface
pages (only widgets render today), voice, and video from the camera.
