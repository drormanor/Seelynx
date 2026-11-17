# Requirements

What you need to run Seelynx: the server, its storage and network, the operating system, the
computers people watch on, and the cameras.

Seelynx records video exactly as the cameras send it, without converting it. So the server needs
little processing power and no graphics card. **Storage and network speed set the limit**, so
size those carefully and the rest follows.

## Server hardware

Choose the server by the number of cameras:

| Cameras | CPU | Memory | Network | Disk write speed |
|---|---|---|---|---|
| Up to 10 | 2 cores | 4 GB | 100 Mbit/s | 5 MB/s |
| Up to 25 | 4 cores | 8 GB | 1 Gbit/s | 13 MB/s |
| Up to 50 | 4 cores | 8 GB | 1 Gbit/s | 25 MB/s |
| Up to 100 | 8 cores | 16 GB | 1 Gbit/s, or 10 Gbit/s with network storage | 50 MB/s |

These figures assume each camera records at 4 Mbit/s. They leave room for the operating system,
viewers, snapshots and exports. For cameras with a higher bitrate, scale the network and disk
write speed to match:

- **Network:** add up the main stream bitrates of all cameras. Keep that total below about 60%
  of the link speed, then add the viewers (see [Network](#network)).
- **Disk write speed:** every 8 Mbit/s of cameras writes 1 MB/s.

Above 100 cameras hasn't been tested.

Any 64-bit Intel or AMD processor from the last several years is enough. A 64-bit ARM build
exists for Docker, but it hasn't been tested.

### Measured results

The load, measured on one machine:
- Xeon w3-2435, 8 cores and 16 threads
- 128 GB of memory
- NVMe disk

CPU is shown as a share of all 16 threads. Memory is the total for the Seelynx server and its
media engine.

**Real cameras**, 2026-11-12. Eight cameras of 3 to 9 megapixels, H.264 and H.265, were measured
over five minutes:

| Video in | CPU | Memory |
|---|---|---|
| 32 Mbit/s | 0.6% | 110 MB |

**Simulated 5-megapixel cameras**, 2026-11-12. Each camera sent 2592×1944 H.264 at 29 frames a
second and 6.5 Mbit/s. Each step ran for a minute after the cameras connected:

| Cameras recording | Video in | CPU | Memory | Disk writes |
|---|---|---|---|---|
| 8 | 52 Mbit/s | 1.1% | 140 MB | 6.4 MB/s |
| 16 | 104 Mbit/s | 1.2% | 195 MB | 12.6 MB/s |
| 32 | 208 Mbit/s | 2.0% | 225 MB | 26.6 MB/s |
| 64 | 416 Mbit/s | 3.7% | 290 MB | 53.7 MB/s |
| 64, plus 16 sub-stream and 4 main-stream viewers | 416 Mbit/s | under 2% | 410 MB | 56 MB/s |

**Simulated 1080p cameras**, 2026-11-06:

| Cameras recording | Video in | CPU | Memory |
|---|---|---|---|
| 25 | 96 Mbit/s | 1.4% | 165 MB |
| 50 | 189 Mbit/s | 2.1% | 207 MB |
| 100 | 378 Mbit/s | 3.7% | 306 MB |

All cameras stayed online with no errors, and the disk writes matched the cameras' bitrate, so
no video was dropped. Each recording camera added about 0.06% CPU and 4 MB of memory. Each
viewer added about 6 MB of memory and no CPU that could be measured, because live video is
passed on as it arrives.

The recommendations above add a wide margin to these results. Some things haven't been tested
yet:
- the smaller machines in the table
- hard disks, and network storage under load
- runs longer than a few minutes at these sizes
- 100 real cameras on a real network

See [ARCHITECTURE.md](ARCHITECTURE.md) for more.

## Storage

### How much space

Recording around the clock uses this much space per camera:

| Camera bitrate | Per day | For 30 days |
|---|---|---|
| 1 Mbit/s | 11 GB | 0.3 TB |
| 2 Mbit/s | 22 GB | 0.65 TB |
| 4 Mbit/s | 43 GB | 1.3 TB |
| 8 Mbit/s | 86 GB | 2.6 TB |

For a whole system at 4 Mbit/s per camera:

| Cameras | 7 days | 14 days | 30 days |
|---|---|---|---|
| 10 | 3 TB | 6 TB | 13 TB |
| 25 | 8 TB | 15 TB | 32 TB |
| 50 | 15 TB | 30 TB | 65 TB |
| 100 | 30 TB | 60 TB | 130 TB |

To work out your own figure in GB, multiply these together:
- the bitrate in Mbit/s
- 10.8
- the days to keep
- the number of cameras

Then add the free space each storage location keeps, which is 50 GB by default.

Bitrate varies a lot between cameras. On one site, real cameras recorded between 0.6 and
10 Mbit/s each, or 6 to 110 GB a day. After a day of recording, check yours on the System page.
Motion-only recording uses less, but how much less depends on how busy each scene is. Size for
continuous recording if you can.

### What kind of storage

- **Keep video off the system disk.** Record to its own disk or array, so a full video disk
  can't stop the operating system.
- **Use disks rated for constant writing,** such as surveillance or NAS drives. Desktop drives
  aren't made for writing around the clock.
- **Protect against a failed disk.** Use RAID 6 or RAID 10 for anything beyond a few cameras.
  RAID 5 with large disks is risky, because a second failure during a rebuild loses
  everything.
- **Network storage** (SMB, NFS or iSCSI) works as a storage location. It has to sustain the disk
  write speed above at all times, while playback and exports read from it as well. Give it its
  own network link if you can (see [Network](#network)). Recording to network storage hasn't
  been tested under full load yet.
- **Standby storage:** set up a local folder as standby on the Storage page. Cameras then
  record there while their usual location is down, such as while network storage reconnects
  after a restart.

## Network

### Bandwidth

The server's network carries three kinds of traffic:
- **Video from the cameras:** the main stream of every recording camera. A camera that's only
  watched, not recorded, connects only while someone watches it.
- **Video to viewers:** each live tile or playback window adds that stream's bitrate. A grid
  uses the smaller sub streams.
- **Network storage**, if you use it: everything recorded is sent on to it.

Give the cameras a wired connection. When the video is kept on network storage, put that on a
separate network link from the cameras. Both carry the full recording bitrate, and sharing one
link halves what each can use.

### Ports

On the Seelynx server:

| Port | Direction | Used for |
|---|---|---|
| TCP 8480 (set with `-listen`) | In, from viewers | The web page and its API. Use HTTPS, with `-tls-cert` and `-tls-key`. |
| UDP 8189 (set with `-webrtc-udp`) | In, from viewers | Live video |
| TCP 554, or the port in each camera's RTSP address | Out, to cameras | Video |
| TCP 80 or 443, to each camera | Out, to cameras | ONVIF: profiles, motion events and pictures |
| UDP 3702, multicast | Out, to the local network | Finding ONVIF cameras. Only works on the same network segment. |
| TCP 445 (SMB), 2049 (NFS) or 3260 (iSCSI) | Out, to storage | Network storage, if used |
| TCP 443, 587 or 465 | Out, to the internet | Only for alarm connections you set up, such as email, Teams or Slack |

Seelynx also uses these ports on 127.0.0.1, which must be free on the server: 18654, 18889,
19996 and 19997. Nothing outside the server needs to reach them.

Don't forward any of these ports to the internet. Use a VPN for viewing from outside, and limit
who can connect under **Settings > Access list**.

## Operating system

| System | Status |
|---|---|
| Windows 11, 64-bit | Tested. Runs as a Windows service, see [WINDOWS-SERVICE.md](WINDOWS-SERVICE.md). |
| Windows 10, 64-bit | Expected to work, not tested |
| Windows Server 2019, 2022 or 2025 | Expected to work, not tested |
| Linux, 64-bit Intel or AMD, with Docker and Compose | Tested on Ubuntu, see [DOCKER.md](DOCKER.md) |
| Linux, 64-bit ARM, with Docker | Builds, not tested |
| macOS | Not supported |

The server also needs:
- **An accurate clock.** Recordings, the timeline and events use the server's time, so keep it
  synchronised with a time server. Set the right time zone too.
- **Administrator rights to install** the Windows service, or Docker on Linux. Day to day,
  nobody needs to sign in to the server itself.
- **Control over restarts.** An automatic update restart stops recording until the server is
  back. On Windows, set active hours, or schedule updates for a quiet time.
- **Antivirus exclusions.** Exclude the recordings folder from real-time scanning. Scanning
  every video file as it's written can slow recording down.

## Viewing

People watch from a web browser. Nothing is installed on their computers.

- **Browser:** an up-to-date Chrome or Edge on a desktop or laptop. Other browsers and phones
  haven't been tested.
- **The viewing computer decodes the video,** not the server. A 16-tile grid has been tested on a
  workstation. Large grids need a reasonably modern computer, ideally with hardware video
  decoding, which most graphics chips have.
- **H.265 cameras** play only in browsers that can decode H.265. H.264 is the safe choice.
- **Fisheye views** need WebGL, which current browsers have.

## Cameras

- **Video:** RTSP, with H.264 or H.265. Recording works with either; for live viewing, see the
  browser note above.
- **Sub stream:** strongly recommended. Grids use it, so they stay light for both the network
  and the viewing computer.
- **ONVIF:** optional. It's used to find cameras, read their stream addresses, receive motion
  events and take pictures. It has been tested on Vicon cameras only.
- **Time:** set the cameras to the same time server as Seelynx, so their events line up with
  the recordings.
