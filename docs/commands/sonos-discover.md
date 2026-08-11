---
title: sonos discover
description: Find every Sonos speaker on the local network and print name, model, and IP.
---

# `sonos discover`

Sends an SSDP M-SEARCH query, then asks the first responder for the full zone topology. Falls back to a subnet scan on networks that block multicast. Names match what the Sonos app shows.

## Synopsis

```
sonos discover [--all] [--format plain|json|tsv] [--timeout 15s]
```

## Flags

| Flag | Default | What it does |
| --- | --- | --- |
| `--all` | off | Include invisible/bonded devices (stereo-pair secondaries, surrounds). |

Plus all [global flags](README.md).

## Examples

```bash
sonos discover
sonos discover --format json
sonos discover --all
sonos discover --format json | jq -r '.[].name'
```

## How it works

1. **SSDP M-SEARCH** for `urn:schemas-upnp-org:device:ZonePlayer:1` on `239.255.255.250:1900`.
2. As soon as one speaker answers, call `ZoneGroupTopology.GetZoneGroupState` on it for the canonical room list.
3. If SSDP returns nothing, scan the local subnet for TCP `1400` and try step 2 against any responder.
4. Filter out invisible/bonded devices unless `--all` is set.

Visible room addresses are cached briefly so subsequent `--name` commands can skip SSDP. Each command still fetches fresh topology before acting, so group changes are respected. An expired or unreachable cache entry falls back to normal discovery.

See [Discovery](../discovery.md) for the full story.
