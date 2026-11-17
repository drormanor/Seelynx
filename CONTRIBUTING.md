# Contributing to Seelynx

Thanks for helping. Bug reports, camera test results, documentation fixes and code are all
welcome.

## Before you start

- **Security problems:** don't open an issue. See [SECURITY.md](SECURITY.md).
- **Bugs and ideas:** open an issue first, so we can agree on the approach before you write
  code. Small fixes can go straight to a pull request.
- **Cameras:** a report that a camera model works, or doesn't, is useful. Include the make,
  model, firmware, codec, and whether ONVIF discovery, live view, recording and events worked.
  Leave out addresses, passwords and anything else from your own network.

## Contributor License Agreement

Before your first pull request can be merged, you need to sign the
[Contributor License Agreement](CLA.md). The CLA check on your pull request tells you how: you
post one comment, once, and it covers all your later contributions.

The CLA lets the project offer Seelynx under other licences as well as the AGPL, while the AGPL
version stays available. You keep the copyright in your work.

## Setting up

Follow [Run it from source](README.md#run-it-from-source-windows) in the README. Linux and NAS
users can work with [Docker](docs/DOCKER.md).

## Checks before a pull request

```powershell
npm --prefix web run build                  # type check and build the client
.tools\go\bin\go.exe vet .\...
.tools\go\bin\go.exe test .\...
```

The CI runs the same checks, and builds the Docker image.

For changes to the web client, also run the matching browser tests in `web\e2e`. **Run them
against a separate copy with an empty data folder, never a system that records real cameras.**
The README shows how to start one.

## Guidelines

- **Never transcode on the server.** Video is stored and forwarded as the camera sends it. See
  [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).
- **Keep it one install.** New features shouldn't need a separate database or service.
- **Protect credentials.** Camera and service passwords must never reach the browser, the logs
  or error messages. Add a test when you touch anything that handles them.
- **Write original work.** Don't copy code, text, screens or assets from other video management
  products, and don't decompile them.
- **Add tests** for new server behaviour, next to the code you change.
- **Write plain text** for the interface and the docs: short sentences, everyday words, and the
  same names the interface uses.
- **Go code** is formatted with `gofmt`. TypeScript follows the style of the files around it.
- **Commit messages** say what the change does, such as "Turn the picture of a camera mounted
  upside down".

## Licence

By contributing, you agree that your work is licensed as described in the [CLA](CLA.md).
Seelynx itself is licensed under the [GNU Affero General Public License v3.0](LICENSE).
