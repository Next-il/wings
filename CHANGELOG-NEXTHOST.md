# NextHost Wings changelog

Changes on top of the upstream base named in UPSTREAM.md. Each section is the body of its GitHub release.

## Unreleased

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

## v1.0.0-rc.1.nh.1

- First NextHost build: pelican-dev/wings v1.0.0-rc.1, unchanged.
- Our release pipeline: Linux amd64 and arm64 binaries with SHA-256 sums on every tag.
