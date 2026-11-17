# Seelynx architecture

Seelynx is an open-source VMS/NVR with a web client, designed for up to 100 IP
cameras on one server.

## Design rules

1. **Never transcode.** Camera video is stored and forwarded as received. Decoding
   100 streams is what would make the server expensive; copying them is cheap.
2. **Record the main stream, view the sub stream.** Each camera has two paths. The
   main stream records continuously. The sub stream connects on demand and feeds
   grid views, because a browser cannot decode dozens of high-resolution streams.
3. **One thing to install.** Config lives in SQLite and video lives in plain files,
   so there is no database server to run.
4. **The media engine is never exposed directly.** Browsers talk only to the Seelynx
   server, which checks the session and then proxies to MediaMTX on localhost.

## Components

```
 IP cameras --RTSP--> MediaMTX --fMP4 segments--> recordings disk
                        ^  |
             control API|  |WebRTC / playback (localhost, internal credentials)
                        |  v
 Browser <--HTTPS--> Seelynx server (Go) ---> SQLite (config, users, events)
 (React client)       auth, cameras, ONVIF discovery, retention, health
```

| Component | Responsibility |
|---|---|
| Seelynx server | Auth, config, API, serves web client, supervises MediaMTX |
| MediaMTX | RTSP ingest, recording, WebRTC live, playback |
| Retention worker | Age-based and disk-pressure deletion, per storage location |
| Storage manager | Spreads cameras over storage locations, and moves them when one fails |
| Web client | Live grid, playback timeline, administration |

## Storage locations

Each camera records to one location at a time. A camera gets a home location when it's added:
the one where one more camera adds the least load for the room it has (disk size less the free
space it keeps). Every 30 seconds each location is checked: its folder must exist, hold the
`.seelynx-storage` marker written when it was added, accept a test write and have at least 1 GB
free. A check that takes over 10 seconds counts as a failure, because a network share that's gone
can hang file calls.

When a location fails, its cameras record to the working location with the most room, and go
back once their home has worked for 2 minutes. MediaMTX finds recordings by a path's name, so a
camera records into `cam_<id>_main` in its current location, and its video in any other location
is renamed to `cam_<id>_main_at<location>`. Each of those folders gets its own playback-only path
in MediaMTX, and Seelynx merges their spans and picks the right one for each request. Files still
open can't be renamed on Windows, so the rename is retried on each check.

Removing a location can first move its video to another. The location stops taking video,
so its cameras leave, and a background job moves its files one at a time: renamed if it's the
same disk, otherwise copied to a `.part` file, synced, renamed into place with its original time,
and only then deleted from the source. Files go into the destination's `cam_<id>_main_at<location>`
folder, which already has a playback path, so every recording can be played back throughout.
Files written in the last minute wait, a file already there with the same size counts as done,
and one with a different size stops the move rather than overwrite it. The destination must have
room for everything above its own free-space target first. The job state is in the database, so it
resumes after a restart, and the location is removed when it's empty.

Retention runs on each readable location separately, with its own free-space target, so a slow
or full location doesn't hold up or empty another.

## First milestone

- First-run admin setup; login with admin, operator and viewer roles
- Add cameras by RTSP URL or ONVIF discovery
- Live grid (1, 4, 9, 16 tiles) over WebRTC
- Continuous recording with per-camera retention and a disk-space floor
- Playback with a day timeline
- System page: camera status, bitrate, disk usage, event log

## Roadmap

Ordered by how much a real deployment needs each item.

| Priority | Feature |
|---|---|
| 1 | Test with real cameras, including ONVIF |
| 1 | Run as a Windows service; installer |
| 2 | Clip export |
| 2 | Audit log |
| 2 | Events and alarms from ONVIF motion |
| 2 | Saved views and multiple view tabs |
| 2 | Health dashboard with history |
| 3 | Recording schedules |
| 3 | PTZ control and presets |
| 3 | Thumbnail search |
| 3 | Backup and restore |
| 3 | Archiving to a NAS or S3 |
| 4 | Multiple recorders and failover |
| 4 | Maps, SSO, SNMP, analytics |

## Capacity estimate

Bandwidth and disk, not CPU, set the limit. At an assumed 4 Mbit/s per main stream:

| Measure | 100 cameras |
|---|---|
| Ingest | 400 Mbit/s |
| Disk write | about 50 MB/s |
| Storage per day | about 4.3 TB |
| Storage for 30 days | about 130 TB |

These are arithmetic estimates.

### Measured

Stepped run, 2026-11-06: simulated 1080p H.264 cameras, all recording, on a Xeon w3-2435
(16 threads), 128 GB RAM, NVMe disk. Each step ran for 2 to 4 minutes, with no errors logged.

| Cameras | Online | Ingest | MediaMTX CPU | MediaMTX memory | Seelynx server CPU and memory |
|---|---|---|---|---|---|
| 25 | 25 | 96 Mbit/s | 1.4% | 98 MB | about 0%, 67 MB |
| 50 | 50 | 189 Mbit/s | 2.1% | 140 MB | about 0%, 67 MB |
| 100 | 100 | 378 Mbit/s | 3.7% | 238 MB | about 0%, 68 MB |

CPU is a share of all 16 threads. This run had no viewers, and the simulated cameras have no
ONVIF events. The recommended hardware is in the
[requirements](REQUIREMENTS.md).

4-minute run, 2026-11-04: 100 simulated 1080p H.264 cameras, all recording, on the same machine.

| Measure | Result |
|---|---|
| Cameras online | 100 of 100 |
| Ingest | 383 Mbit/s |
| MediaMTX CPU | 1.8% of the machine |
| MediaMTX memory | 259 MB |
| Seelynx server CPU and memory | about 0%, 22 MB |
| Engine errors during the run | 0 |
| Sampled recordings (3 cameras) | Decoded with no errors |
| 16-tile browser test during the run | Passed |

This shows the software design holds at 100 cameras. It does not yet prove a
deployment: video travelled over loopback from one simulated source, so it did not
exercise a real network, 100 different camera firmwares, or camera reconnects. The
run was too short to show memory stability or retention at volume.

## Clean-room rule

Seelynx's code, screens and wording are original. Do not decompile other products or copy
their code, bundled scripts, database schemas, icons, or other assets into this repository.
