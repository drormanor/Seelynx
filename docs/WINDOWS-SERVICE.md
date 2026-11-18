# Running Seelynx as a Windows service

As a service, Seelynx starts with Windows, keeps running after you sign out, and restarts by
itself after a crash.

Before you start, check the [requirements](REQUIREMENTS.md) for your
number of cameras.

Open PowerShell with **Run as administrator**, then go to the Seelynx folder: the unzipped
[release](https://github.com/drormanor/Seelynx/releases), or your copy of the source after building
it.

## Install

From a release:

```powershell
powershell -ExecutionPolicy Bypass -File .\service.ps1 install -DataDir C:\ProgramData\Seelynx -MinFreeGB 50
```

From the source, the script is in `scripts`:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\service.ps1 install -DataDir C:\ProgramData\Seelynx -MinFreeGB 50
```

| Option | Default | Purpose |
|---|---|---|
| `-DataDir` | `C:\ProgramData\Seelynx` | Database, logs, backups and pictures |
| `-RecordingsDir` | `<DataDir>\recordings` | The first storage location, made on the first start |
| `-MinFreeGB` | `50` | Free space the first storage location keeps, by deleting the oldest recordings |
| `-Listen` | `127.0.0.1:8480` | Web server address. Use `:8480` to let other computers connect. |

After the first start, manage storage on the **Storage** page instead. Changing these options
later doesn't change the locations.

To keep an existing system, set `-DataDir` to its current data folder. The script stops any
copy of Seelynx that's already running.

The script copies Seelynx, MediaMTX and ffmpeg (if present) to `C:\Program Files\Seelynx`, creates the service, and gives
it permission to use your data folder. The service uses its own limited account,
`NT SERVICE\Seelynx`, rather than the all-powerful LocalSystem.

## Everyday commands

```powershell
Restart-Service Seelynx                  # for example, to finish a restore
Stop-Service Seelynx
Start-Service Seelynx
.\scripts\service.ps1 status             # state, account and recent errors
```

## Upgrade

Unzip the new release into a new folder and run this there as administrator:

```powershell
powershell -ExecutionPolicy Bypass -File .\service.ps1 upgrade
```

From the source, build the new version into `bin` first, then run `.\scripts\service.ps1 upgrade`.

It stops the service, backs up the database, copies the new programs and starts it again.

## Uninstall

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\service.ps1 uninstall
```

Your data folder and recordings are not deleted.

## If it won't start

1. Run `.\scripts\service.ps1 status` to see recent errors.
2. Look in `seelynx.log` in the data folder.
3. If it's a permissions error, check that `NT SERVICE\Seelynx` has Modify access to the data
   folder.

## Network storage

The service account connects to other computers as this computer, not as you, so a mapped
drive letter won't work. On the **Storage** page, use a full network path such as
`\\nas\video`, and give this computer's account access to the share. Give `NT SERVICE\Seelynx`
Modify access to any local folder you add as a location.

## Other computers

Video for other computers uses UDP port 8189, so allow it through Windows Firewall when you
open Seelynx to them. See the README for the web server options.
