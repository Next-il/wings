# NextHost Wings changelog

Changes on top of the upstream base named in UPSTREAM.md. Each section is the body of its GitHub release.

## Unreleased

- Staff shell: `GET /api/servers/:server/shell` is a websocket with a Linux shell in the server's running
  container - `docker exec` through Wings' own Docker client, under a TTY, as the container's user (`bash`,
  falling back to `sh`, both as login shells). NextHost's server page uses it for running servers instead of
  its node agent.
  - Authentication: the first frame is `{"type":"auth","token":"<jwt>"}`. The JWT is signed with the node
    token like the console's, but has its own scope `shell` (and only that scope) and must list the
    `admin.shell` permission as is - `*` never grants it, so no console token can open a shell, and a shell
    token cannot open the console. It must be for this server, issued after Wings booted, live at most 15
    minutes, and it is spent on first use. NextHost signs one only for staff with `@hosting/shell`.
  - Frames are NextHost's shell frames: JSON text frames `input`, `resize` and `ping` from the page;
    terminal output as binary frames, and `ready`, `exit`, `error` (`UNAUTHORIZED`, `NOT_RUNNING`, `BUSY`,
    `FAILED`) and `pong` as JSON text frames. `?cols=&rows=` is the size it opens at.
  - One session per server; at most 4 sockets per server at a time, and 10 seconds to authenticate. A
    session closes after 30 minutes without input, when its socket closes (Wings pings it every 30 seconds
    and drops it after 90 without an answer), when the shell exits, or when the server is deleted. A
    stopped container is refused (`NOT_RUNNING`): Wings never starts one for a shell.
  - Docker does not end a TTY exec when its client goes away, so every session's exec carries
    `NEXTHOST_SHELL_SESSION=wings-<uuid>` in its environment. On close Wings sends `SIGHUP` to every process
    in the container's pid namespace that carries it, then `SIGKILL` after 2 seconds; when the shell exits by
    itself, what it left behind is killed. At boot Wings kills what sessions of an earlier run left in any
    container (values starting with `wings-` only - NextHost's node agent uses the same variable for its own
    sessions without the prefix).
  - Activity: `server:shell.open` and `server:shell.close`, with the token's `session_id`; the close also
    carries `duration_seconds`, `reason` (`exit`, `idle`, `closed`) and `exit_code`. Nothing typed or printed
    is logged.
- `GET /api/system` reports `features` - what this build adds that NextHost checks before using it, so it
  never has to compare version numbers. Now `["shell"]`.

## v1.0.0-rc.1.nh.1

- First NextHost build: pelican-dev/wings v1.0.0-rc.1, unchanged.
- Our release pipeline: Linux amd64 and arm64 binaries with SHA-256 sums on every tag.
