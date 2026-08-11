---
title: Discovery
description: How sonoscli finds your speakers — SSDP, ZoneGroupTopology, and the subnet-scan fallback.
---

# Discovery

Finding speakers reliably on real-world networks is the trickiest part of any Sonos client. `sonoscli` uses three layers, in order, and falls back gracefully.

## 1. SSDP M-SEARCH (fast path)

```
Multicast: 239.255.255.250:1900
Query:     M-SEARCH * HTTP/1.1
ST:        urn:schemas-upnp-org:device:ZonePlayer:1
```

Each responding speaker returns a `LOCATION` header that points at:

```
http://<speaker-ip>:1400/xml/device_description.xml
```

We don't need *every* speaker to answer SSDP — we just need **one**. As soon as we have a foothold, we ask that speaker for the full zone topology.

SSDP fails on networks that block multicast (some enterprise / mesh / VLAN setups, flaky Wi-Fi), which is why it's never the only path.

## 2. ZoneGroupTopology (source of truth)

Once we have any IP, we call:

```
SOAP: ZoneGroupTopology.GetZoneGroupState
On:   http://<ip>:1400/ZoneGroupTopology/Control
```

The response is an XML document with the canonical view of the system: every zone, its coordinator, group members, and bonded satellites. This matches what the Sonos app shows.

`sonoscli` parses this into Go structs once per command. It caches room addresses as discovery footholds, but always fetches fresh topology before acting.

## 3. Subnet scan (last resort)

If SSDP returns nothing — e.g. multicast is filtered — `sonoscli` enumerates the local IPv4 subnet, opens a fast TCP probe to port `1400`, and treats anything that returns valid `device_description.xml` as a foothold. Then it goes back to step 2.

This is slower (parallel scan, capped concurrency) but tends to find speakers when SSDP can't.

## Why not just trust SSDP?

A naive "list every SSDP responder" approach has two problems:

- **Missing speakers**: SSDP packets get dropped; satellites and bonded secondaries don't always answer.
- **Phantom speakers**: bonded surrounds and stereo-pair secondaries do answer, but they don't represent rooms — they represent components of a room.

Topology gives the same answer the Sonos app gives. By default `sonoscli` shows only "visible" zones. Pass `--all` to `discover` or `group status` to see bonded/invisible devices for debugging.

## Caching

Discovered visible-room names and addresses are cached locally so:

- Subsequent `--name` commands can skip SSDP when the cache is fresh.
- Every command still fetches current topology, so regrouping is respected.
- Expired or unreachable entries automatically fall back to full discovery.
- Shell completion for `--name` works without network calls.

The cache lives in your platform's user cache directory. Running `sonos discover` refreshes it.

## Tips for flaky networks

- Pin the IP: `sonos status --ip 10.0.0.42` skips discovery entirely.
- Default discovery waits up to 15 seconds. Use a higher `--timeout` on especially slow Wi-Fi, or `sonos config set defaultTimeout 20s` to make it sticky.
- Make sure your machine and your Sonos are on the same VLAN — Sonos does not route across L3 boundaries by default.
- For UPnP eventing (`sonos watch`), your machine must be reachable from the speaker on the chosen callback port; firewall prompts are expected.
