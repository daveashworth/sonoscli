---
title: Architecture
description: How sonoscli is organized — packages, command flow, coordinator resolution, and the SOAP client.
---

# Architecture

`sonoscli` is a small Go binary that speaks Sonos's UPnP/SOAP dialect over the LAN. It is built from a handful of packages that you can read in one sitting.

## Package layout

```
cmd/sonos/                 # main entrypoint (just calls into internal/cli)
internal/cli/              # Cobra commands, flag plumbing, output formatting
internal/sonos/            # SOAP client, SSDP, topology, AVTransport, ContentDirectory, RenderingControl
internal/spotify/          # Optional Spotify Web API client (client credentials only)
internal/streamproxy/      # Short-lived local MP3 proxy for play-url
internal/scenes/           # Scene save/apply (grouping + per-room volume/mute)
internal/appconfig/        # Local config file (~/.config/sonoscli/config.json)
```

## Command flow

Every command follows the same path:

1. **Parse flags** with Cobra. Global flags: `--name`, `--ip`, `--format`, `--debug`, `--timeout`.
2. **Resolve target** — when a command needs a speaker:
   - `--ip` is used directly.
   - Otherwise a cached room address is used to fetch fresh topology. Discovery runs when the cache is empty, expired, or unreachable.
3. **Resolve coordinator** — transport-affecting commands (`play`, `pause`, `next`, queue, etc.) walk the topology and replace the target with its group coordinator. This is why a `pause` aimed at a satellite still pauses the whole group.
4. **Issue SOAP** — a tiny SOAP client posts to `http://<coordinator-ip>:1400/MediaRenderer/AVTransport/Control` (or whichever service is needed) with the right `SOAPAction` header.
5. **Format output** — `--format plain|json|tsv` runs through one rendering layer so every command prints consistently.

## Topology is the source of truth

Sonos exposes the real grouping state via `ZoneGroupTopology.GetZoneGroupState`, which returns an XML blob that lists every zone, its coordinator, members, and bonded satellites. `sonoscli`:

- Treats topology — not SSDP — as canonical for the room list.
- Filters bonded satellites and stereo-pair secondaries from default output (use `--all` to include them).
- Caches the parsed topology for a short TTL so subsequent commands skip discovery.

See [Discovery](discovery.md) for the SSDP details.

## SOAP client

`internal/sonos` ships a minimal SOAP client that:

- Formats request envelopes for `urn:schemas-upnp-org:service:*` actions.
- Sends them with the right `SOAPAction` header.
- Parses response envelopes into Go structs per action.
- Surfaces UPnP fault codes as Go errors.

Only the actions actually needed by the CLI are wired up. Adding a new action is mostly schema plumbing.

## Eventing

`sonos watch` uses UPnP eventing (GENA): it starts a small HTTP server, sends `SUBSCRIBE` requests for `AVTransport` and `RenderingControl`, renews finite subscription leases before they expire, and re-renders state changes as they stream in. The callback URL must be reachable from the speaker, which is why your firewall may prompt on first run.

## Stream proxy

`sonos play-url` starts a detached local daemon for web audio. The foreground command resolves the target speaker, chooses a LAN-reachable local IP, writes a one-shot proxy config, waits for a tokenized health check, then points Sonos at the generated `http://<local-ip>:<port>/...mp3` URL.

The daemon keeps Sonos on a simple MP3 stream while your machine handles the messy side:

- direct media URLs go through `ffmpeg`;
- YouTube and other media pages use `yt-dlp` when needed;
- HLS-only YouTube formats are downloaded by `yt-dlp` and piped into `ffmpeg`;
- YouTube / YouTube Music playlists expose one local MP3 path per track, then queue those paths with Sonos metadata.

The proxy exits after EOF or an idle timeout, so normal `play-url` usage does not leave a permanent server behind.

## Output

Three machine modes plus a human mode:

- `plain` — the default; tuned for terminals.
- `json` — stable shape; suitable for `jq` and dashboards.
- `tsv` — easy `cut`/`awk`-able row format.

Errors always go to stderr with a non-zero exit code. `--debug` adds a structured trace including SOAP requests and responses, redacted where appropriate.

## Configuration

Local defaults live at `~/.config/sonoscli/config.json` (or the platform equivalent). The file is small on purpose — `sonos config get|set|unset|path` is the only supported way to write it.

## Scenes

Scenes are stored as JSON next to the config:

- Capture: walk the topology, snapshot each visible zone's coordinator, members, volume, and mute.
- Apply: dissolve current groups, re-create the saved groups, then re-apply per-room volume/mute.

Apply is best-effort and idempotent — running `scene apply evening` twice is safe.
