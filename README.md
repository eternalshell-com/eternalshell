# EternalShell

A small terminal tool that shows what is running on a Linux server and which app it belongs to.

> Work in progress. Built for Ubuntu servers (amd64).

## What it does

- Live terminal UI with two views:
  - **Containers**, grouped by Docker Compose project, with CPU, memory and logs (Enter).
  - **Processes**, with the origin of each process: container, systemd service or host.
- `eternalshell status` prints a one-shot summary: CPU, load, memory, disks, top processes.
- Read-only: it changes nothing on the server and opens no ports.

Planned: email alerts for sustained CPU load, unknown SSH logins and full disks.

## Build

Requires Go.

```sh
CGO_ENABLED=0 go build -o eternalshell ./cmd/eternalshell
```

Copy the binary to your server and run it:

```sh
./eternalshell          # live UI
./eternalshell status   # one-shot summary
```

Container data needs access to the Docker socket (for example, a user in the `docker` group).
