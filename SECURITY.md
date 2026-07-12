# Security policy

## Reporting a vulnerability

Please report security issues **privately** — do not open a public issue.

Use GitHub's private vulnerability reporting:
[**Report a vulnerability**](https://github.com/tankertop/tankertop/security/advisories/new).
You'll get a response as soon as possible, and disclosure will be coordinated
with you once a fix is available.

## Supported versions

Security fixes target the **latest release**. Please upgrade before reporting.

## What tankertop can and can't do

tankertop is a read-and-manage client — it holds no long-lived state and runs
with **your** credentials:

- It reads your kubeconfig / talks to the container engine as **you**. It cannot
  do anything you couldn't already do with `kubectl` / `docker`; it grants no new
  access.
- The **shell** (`S`), **inspect** (`i`), the **env pane's** runtime read, the
  **filesystem browser** (`f`) and **copy** (`c`) all use `exec` into the
  container. They need the same `pods/exec` (Kubernetes) or engine access you'd
  need to run those commands by hand.
- Masking of credential-looking values in the env pane is **shoulder-surfing
  protection, not access control.** Anyone who can run tankertop against the
  target can already read those values; `m` reveals them.
- `--ssh` shells out to your system `ssh`. tankertop never sees, stores, or
  transmits a password or key — authentication is entirely OpenSSH's (agent,
  `~/.ssh/config`, `known_hosts`, 2FA).
- Only preferences (theme, sort, tree, namespace, interval) are written to disk,
  under `$XDG_CONFIG_HOME/tankertop/`. No secrets are persisted.

## Verifying downloads

Every release publishes `checksums.txt`. The `install.sh` script verifies the
download automatically; you can verify any asset yourself with
`sha256sum -c checksums.txt` (or `shasum -a 256`).
