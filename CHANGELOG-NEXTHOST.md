# NextHost Wings changelog

Changes on top of the upstream base named in UPSTREAM.md. Each section is the body of its GitHub release.

## v1.0.0-rc.1.nh.5 (unreleased)

- **Domain routes from files, applied live:** Wings no longer labels a server's container for Traefik.
  It writes one file per server, `<proxy data_dir>/dynamic/<server uuid>.yml` (by default
  `/var/lib/pelican/proxy/dynamic/`), which the proxy watches.
  - The file is written when the server starts, and again every time NextHost calls the server's sync
    endpoint (`POST /api/servers/:server/sync`) while it runs: a domain change applies a few seconds
    after the sync, without restarting the server. It is removed when the server stops or is deleted, or
    has no domains left. At boot Wings removes the files of servers that are no longer on the node.
  - Router and service names are `nh-<server uuid>-<n>` (the whole uuid now), with `-http`, `-https`,
    `-strip` and `-prefix` for an entry's plain HTTP router and its middlewares.
- **Domain options:** every new field is optional. An entry with only `domain` and `port` works as before:
  HTTPS with a Let's Encrypt certificate on the whole host, plain HTTP redirected to it.

  ```json
  "domains": [{ "domain": "example.com", "port": 8443, "path": "/api", "strip_path": true,
                "http_only": false, "scheme": "https", "dest_path": "/v1" }]
  ```

  - `path`: the URL path prefix the entry matches, `/` when empty. It starts with `/`, is at most 200
    characters of letters, digits and `-._~!$&()*+,;=:@%/`, and has no empty, `.` or `..` part.
  - `strip_path`: take `path` off the request before it reaches the app.
  - `http_only`: serve the entry on plain HTTP only - no certificate, no redirect to HTTPS.
  - `scheme`: how the proxy talks to the app, `http` (default) or `https` for an app that serves TLS
    itself. Its certificate is not checked, so a self-signed one works.
  - `dest_path`: a prefix put in front of the path (after `strip_path`) before the request reaches the
    app. `""` or `/` for none.
  - An invalid entry, or a second entry for the same domain and path, is dropped with a warning in the
    Wings log. It never fails a start.
- **The proxy:** Traefik also reads the routes folder (mounted read-only at `/etc/traefik/dynamic`,
  watched). Plain HTTP is no longer redirected on the entrypoint: `_nexthost.yml` in the same folder
  redirects every host to HTTPS at the lowest priority, so only `http_only` entries stay on HTTP. Let's
  Encrypt's HTTP challenge on port 80 works as before. The Docker provider stays on, so a container made
  by nh.3 or nh.4 keeps its label routes until its next start recreates it without labels.
  - Upgrading recreates the proxy container once (its settings changed), so domains are down for a few
    seconds while Wings boots.
- `GET /api/system` reports `proxy.features`: `["paths", "http-only", "scheme", "live"]` when the running
  proxy container is the one this build makes, `[]` otherwise.
- **App storage:** an app can name its storage instead of taking the whole server folder at `data_path`:

  ```json
  "app": {
    "enabled": true,
    "volumes": [{ "name": "data", "target": "/data" }],
    "files": [{ "name": "config", "target": "/app/config.json", "content": "<base64>", "mode": 420 }]
  }
  ```

  - No `volumes` key (or `null`) keeps the nh.4 layout. A `volumes` list, even an empty one, replaces it:
    the server folder itself is not mounted, and each volume is the folder `<server folder>/<name>`,
    created when missing and opened to the image's user (0777), mounted read-write at `target`.
  - Each file is written to `<server folder>/.files/<name>` before every start (base64 `content`, `mode`
    as a number, 0644 when 0) and mounted read-only at `target`, a file path. Files no longer listed are
    removed from `.files`. Files work with either layout.
  - Names are 1-40 of `a-z 0-9 - _`, starting with a letter or digit, unique among volumes and among
    files. A target is a clean absolute path, not `/` or one of `/bin /boot /dev /etc /lib /lib32 /lib64
    /libx32 /proc /run /sbin /sys /tmp /usr /var/run` itself, and nothing under `/proc`, `/sys` or `/dev`
    (`/etc/nginx/conf.d/default.conf` is fine). No two volumes or files share a target, and none takes
    the folder's or another mount's. At most 10 volumes and 20 files, 256 KiB per file. An invalid entry
    is dropped with a warning in the Wings log and the server console; it never fails a start.
  - Volumes and files are mounted as subpaths of one Docker volume of the server folder
    (`nexthost-app-<server uuid>`, local driver bound to the folder), not as bind mounts: a customer can
    put a symlink in the folder over SFTP, and a bind mount would follow it on the host. Docker resolves a
    subpath inside the folder. This needs Docker Engine 26 or newer (API 1.45); on an older engine an app
    with volumes or files does not start. The volume is removed with the server. A symlink where a
    volume's folder goes fails the start; one where a file goes is replaced by the file.
  - `app-storage` added to the features list (header and `/api/system`).
- **Port 80 for non-root images:** an app container gets `net.ipv4.ip_unprivileged_port_start=0`, so an
  image whose `USER` is not root can listen on 80 (or any port under 1024) inside. Not set on the host's
  network.

## v1.0.0-rc.1.nh.4

- **App servers:** a server can run an application image - one NextHost built on the node from the
  customer's Dockerfile, or pulled from a registry - the way Docker runs it anywhere else. NextHost sends:

  ```json
  "app": { "enabled": true, "data_path": "/data" }
  ```

  - The process runs as the image's own user, not Wings' user.
  - The root filesystem is writable. The container is recreated on every start, so it is the app's scratch
    space; what it keeps goes in the server's folder.
  - The server's folder is mounted at `data_path` (default `/data`; never over a system folder) instead of
    `/home/container`, and the image keeps its own `/etc/passwd` and `/etc/group`.
  - Docker's default capabilities without `setpcap`, `mknod`, `audit_write`, `net_raw`, `setfcap`,
    `sys_chroot`, `sys_ptrace` - so an entrypoint that chowns its files or switches user works, and so does
    an app listening on a port under 1024 inside.
  - The folder is not chowned to Wings' user before a start (its files belong to the image's user) and is
    opened to that user (0777; only this server's container mounts it).
  - Unchanged: no-new-privileges, resource limits, network, port mappings, domains. A locally built image is
    named with Wings' existing `~` prefix (`~nexthost-app/<server>:<commit>`), so it is never pulled.
- A server whose egg has no startup "done" lines is marked running as soon as its container starts, instead of
  staying "starting" until it stops.

## v1.0.0-rc.1.nh.3

- **Custom domains:** a customer's web app in a server (a Next.js app on port 3000 inside, say) is reached
  at `https://app.example.com` with no port. The customer points an A record at the node; NextHost checks it
  and only then sends the domain in the server configuration:

  ```json
  "domains": [{ "domain": "app.example.com", "port": 3000 }]
  ```

  - `port` is the port the app listens on inside the container. The app must listen on `0.0.0.0`.
  - With the proxy enabled, Wings labels the server's container for Traefik when it creates it (the next
    start, like the port mapping): router and service `nh-<first 8 of the server uuid>-<n>`, rule
    `Host(\`<domain>\`)`, entrypoint `websecure`, certificate resolver `le`, over the Wings network.
  - A domain that is not a plain lowercase hostname (letters, digits, hyphens, dots; no wildcard) or a port
    outside 1-65535 is dropped with a warning; it never fails the start. A server's own `labels` cannot set
    anything under `traefik.` - Wings removes those, so one server cannot route another's domain.
- **The proxy:** a `proxy` section in config.yml, on unless turned off:

  ```yaml
  proxy:
    enabled: true
    image: traefik:v3.7.15
    container_name: nexthost-proxy
    http_port: 80
    https_port: 443
    acme_email: ""
    data_dir: /var/lib/pelican/proxy   # acme.json
  ```

  - Enabled, Wings creates the `nexthost-proxy` container at boot through its own Docker client and keeps it
    running (`unless-stopped`, so it outlives Wings restarts): Traefik with the Docker provider (only
    containers that opt in), on the Wings network, ports 80 and 443 on every interface, plain HTTP
    redirected to HTTPS, one Let's Encrypt resolver `le` (HTTP challenge on port 80), the Docker socket
    mounted read-only, no dashboard or API. A changed setting recreates it; turned off, an existing one is
    left alone.
  - When something else holds port 80 or 443 (nginx left by a `certbot --nginx` install), the container
    does not start: Wings logs it and runs on. The NextHost Doctor's "Domain proxy" check names what holds
    the port, and its fix takes nginx off and starts the proxy.
  - Port 80 is the proxy's now: a node that renews Wings' own certificate with `certbot --standalone` has to
    stop the proxy container around renewals. The NextHost agent installs certbot pre and post hooks for that.
- `GET /api/system` reports `proxy`: `{ "enabled": bool, "running": bool, "image": string }`.
- `domains` added to the features list (header and `/api/system`).

## v1.0.0-rc.1.nh.2

- **Port mapping:** a server's allocation can point at another port inside the container, so a database or
  game listens on its default port (3306, 5432, 27017, ...) while players connect to any allocation.
  The server configuration from the remote takes an optional `allocations.container_ports`, keyed by IP and
  then by the allocation's port:

  ```json
  "allocations": {
    "default": { "ip": "10.0.0.1", "port": 43666 },
    "mappings": { "10.0.0.1": [43666, 43667] },
    "container_ports": { "10.0.0.1": { "43666": 3306 } }
  }
  ```

  - Docker binds `10.0.0.1:43666` (tcp and udp) to `3306` in the container. An allocation without an entry
    keeps the same port inside, so a remote that does not send the field gets upstream's behaviour, and
    upstream Wings ignores the field. Values outside 1-65535 are ignored.
  - `SERVER_PORT` (the environment variable and `{{SERVER_PORT}}` in the startup command) is the default
    allocation's port inside the container - what the server must listen on.
  - New `SERVER_PUBLIC_PORT` (and `{{SERVER_PUBLIC_PORT}}`): always the default allocation's port as clients
    reach it, for a server that has to advertise its address. Set on every server; an egg variable of that
    name is dropped, like one named `SERVER_PORT`.
  - `SERVER_IP`, `force_outgoing_ip` and the `127.0.0.1` -> Docker interface rewrite are unchanged. With the
    `macvlan` network driver there are no port bindings, so a mapping does nothing there.
  - A changed mapping applies when the container is recreated, i.e. at the next start.
- **Feature header:** every remote API request carries `X-NextHost-Features` (comma separated, today
  `container-ports`) so NextHost only sends a feature to a node whose Wings understands it. A
  `remote_query.custom_headers` entry in config.yml cannot override it.
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
- `GET /api/system` reports `features`, the same list the feature header sends - what this build adds that
  NextHost checks before using it, so it never has to compare version numbers. Now
  `["container-ports", "shell"]`.

## v1.0.0-rc.1.nh.1

- First NextHost build: pelican-dev/wings v1.0.0-rc.1, unchanged.
- Our release pipeline: Linux amd64 and arm64 binaries with SHA-256 sums on every tag.
