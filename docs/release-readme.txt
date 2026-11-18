Seelynx for Windows
===================

Open-source video management for IP cameras. Project page, documentation and help:
https://github.com/drormanor/Seelynx

What's in this folder
---------------------
seelynx.exe            The Seelynx server
mediamtx.exe           The media engine Seelynx runs (MIT license, see MEDIAMTX-LICENSE.txt)
service.ps1            Installs Seelynx as a Windows service
LICENSE.txt            Seelynx's license, the GNU AGPL v3

Try it
------
1. Open PowerShell in this folder.
2. Run:   .\seelynx.exe -mediamtx .\mediamtx.exe
3. Open http://127.0.0.1:8480 in Chrome or Edge, and create the administrator account.
4. On the Cameras page, find your cameras with ONVIF, or add them by RTSP address.

Recordings and settings go in a "data" folder next to seelynx.exe. Press Ctrl+C to stop.

Run it as a Windows service
---------------------------
To start Seelynx with Windows and keep it running, open PowerShell with "Run as
administrator" in this folder and run:

  powershell -ExecutionPolicy Bypass -File .\service.ps1 install -DataDir C:\ProgramData\Seelynx

To let other computers connect, add  -Listen :8480  and allow TCP port 8480 and UDP port 8189
through Windows Firewall. See docs/WINDOWS-SERVICE.md on the project page for the details.

Better snapshots
----------------
Seelynx can read alarm snapshots from its own recordings using ffmpeg, which isn't included.
Put ffmpeg.exe next to seelynx.exe (or in C:\Program Files\Seelynx for the service). Without it,
snapshots come from the cameras and can arrive late.

Before you rely on it
---------------------
Seelynx is at an early stage. Check docs/REQUIREMENTS.md on the project page to size the server
and storage, and keep a copy of data\secret.key somewhere safe.
