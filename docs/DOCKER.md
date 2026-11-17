# Running Seelynx in Docker

Docker suits a Linux server or a NAS. The image includes Seelynx, MediaMTX and ffmpeg, runs as
an ordinary user rather than root, and restarts by itself.

## Start it

You need Docker with Compose, on Linux. Check the
[requirements](REQUIREMENTS.md) for your number of cameras first.

```sh
git clone <the Seelynx repository>
cd Seelynx
echo "TZ=America/Los_Angeles" > .env
mkdir -p seelynx-data seelynx-recordings
sudo chown -R 1000:1000 seelynx-data seelynx-recordings
SEELYNX_VERSION=$(git rev-parse --short HEAD) docker compose up -d --build
```

Setting `SEELYNX_VERSION` makes the version next to Help match the commit you built. Without it the app shows `Version dev`.

Open `http://<server address>:8480` and create the administrator account.

Set `TZ` to your time zone, because schedules and recording names use it.

Seelynx deletes the oldest video to keep 50 GB free by default. On a smaller disk, set a lower
figure before you start, such as `SEELYNX_MIN_FREE_GB=5` in `.env`. If the figure is more than the
whole disk, Seelynx keeps 10% free instead and warns on the System page.

Ubuntu's installer may use only half of a large disk. Check with `df -h /`, and if you need the
rest, run:

```sh
sudo lvextend -r -l +100%FREE /dev/ubuntu-vg/ubuntu-lv
```

## Protect it

Unlike the Windows default, this listens on your network, and plain HTTP sends passwords
unencrypted. Before relying on it:

- Use HTTPS. Put your certificate and key in the data folder, then add
  `"-tls-cert", "/data/cert.pem", "-tls-key", "/data/key.pem"` to `command` in
  `docker-compose.yml`.
- Limit who can connect under **Settings > Access list**.
- Don't forward these ports to the internet.

## Where things are kept

| Folder on the server | Holds |
|---|---|
| `seelynx-data` | Database, logs, backups, pictures and `secret.key` |
| `seelynx-recordings` | Video |

Keep a copy of `seelynx-data/secret.key`. Without it, saved camera passwords and connection
secrets can't be read.

The container runs as user 1000, which is why those folders are created and handed to it first.

## Record to network storage

Yes, this works. Keep `seelynx-data` on a local disk, because the database doesn't work reliably
over a network share, and put only the video on the share.

**NFS.** Add this to `.env`:

```sh
NAS_ADDRESS=192.168.1.50
NAS_PATH=/volume1/seelynx
```

```sh
docker compose -f docker-compose.yml -f docker/nfs.yml up -d --build
```

The share must let user 1000 write.

**SMB, such as a Windows share.** Add this to `.env`:

```sh
NAS_ADDRESS=192.168.1.50
NAS_SHARE=seelynx
NAS_USER=seelynx
NAS_PASSWORD=<the share's password>
```

```sh
docker compose -f docker-compose.yml -f docker/smb.yml up -d --build
```

**A folder your server already mounts.** Set `SEELYNX_RECORDINGS=/mnt/nas/seelynx` in `.env` and
use the plain `docker compose up -d --build`.

If the share goes offline, recording stops until it returns, so add a **Storage problem** or
**Camera offline** rule to hear about problems. To keep recording through an outage, use a local
folder as the first location and add the share as a second one, as below. Set `SEELYNX_MIN_FREE_GB` to how much free space
to keep on the share. Check that the share is fast enough: each camera writes a few Mbit/s, all
day.

## More than one storage location

Seelynx can record to several locations, such as a local disk and a NAS. It spreads the cameras
across them, and if one stops working its cameras record to the others until it's back. Manage
them on the **Storage** page.

Inside Docker, Seelynx only sees folders added to the container. `docker/storage2.yml` adds one:

1. Mount the share on the server so it's connected after a restart, for example in `/etc/fstab`:

   ```
   192.168.1.50:/volume1/seelynx  /mnt/nas  nfs  rw,nfsvers=4,soft,timeo=100,retrans=3,_netdev,nofail  0  0
   ```

   `soft` makes file calls fail if the share goes away, rather than wait for it forever. `nofail`
   lets the server start without it.

2. Let user 1000 write to it: `sudo chown 1000:1000 /mnt/nas`.
3. Add `SEELYNX_STORAGE2=/mnt/nas` to `.env`, and start Seelynx with:

   ```sh
   docker compose -f docker-compose.yml -f docker/storage2.yml up -d --build
   ```

4. On the **Storage** page, choose **Add location** and enter `/storage2`.

If the share isn't mounted, the container sees an empty folder. Seelynx notices that the marker
file it put there is missing, and records to the other locations instead of filling the local disk.

## Upgrade

```sh
git pull
docker compose up -d --build
```

## Docker Desktop on Windows or Mac

Host networking isn't the same there, so live video and finding cameras may not work. On Windows,
use the [Windows service](WINDOWS-SERVICE.md) instead.

## Other processors

The image builds for 64-bit Intel and AMD by default. For a 64-bit ARM server, add this under
`build` in `docker-compose.yml`. The ARM build hasn't been tested:

```yaml
build:
  context: .
  args:
    MEDIAMTX_ARCH: linux_arm64
```
