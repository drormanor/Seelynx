# Security policy

Seelynx records cameras and stores their passwords, so security problems matter. Thank you for
reporting them responsibly.

## Reporting a vulnerability

**Don't open a public issue.** Report it privately on GitHub instead:

1. Go to the **Security** tab of this repository.
2. Choose **Report a vulnerability**.
3. Describe the problem, the version (shown next to Help in the app), and how to reproduce it.

You should get a reply within 7 days. Once the problem is confirmed, a fix is prepared before
anything is published, and you're credited in the advisory unless you'd rather not be.

## Supported versions

Seelynx is at an early stage and has no stable releases yet. Fixes go into the `main` branch
only, so keep your copy up to date.

## In scope

For example:
- signing in without a valid account, or getting around a role's permissions
- reading camera passwords, connection secrets or other users' data
- reaching the media engine or another internal port from outside the server
- running code or reading files on the server through the web interface or API

Problems in the cameras themselves, in MediaMTX or in ffmpeg should go to those projects. Tell
us too if Seelynx needs to change because of them.

## Running Seelynx safely

- Keep it on a private network. Don't forward its ports to the internet; use a VPN for remote
  viewing.
- Use HTTPS with `-tls-cert` and `-tls-key` whenever other computers connect.
- Limit who can connect under **Settings > Access list**.
- Protect the `data` folder, and keep a copy of `data\secret.key` somewhere safe and separate.
