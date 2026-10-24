# NextHost Wings

The daemon that runs game and database servers on NextHost's nodes. It started from
[Pelican Wings](https://github.com/pelican-dev/wings) v1.0.0-rc.1 and is maintained here as NextHost's own
project: our features go straight into this code, and upstream changes are reviewed and ported by hand
(see [UPSTREAM.md](UPSTREAM.md)).

- **Remote API:** answered by NextHost (`https://api.next-il.co.il/servers/wings`), not a Pelican panel.
  `config.yml` and the remote API stay compatible with Pelican's, so a node switches builds without other changes.
- **Releases:** a `v<upstream base>.nh.<n>` tag builds Linux amd64 and arm64 binaries with SHA-256 sums
  (`.github/workflows/release.yaml`). The NextHost node agent installs the version NextHost pins.
- **Changes on top of upstream:** [CHANGELOG-NEXTHOST.md](CHANGELOG-NEXTHOST.md).
- **License:** MIT, as upstream - the original copyright notice in [LICENSE](LICENSE) stays.

Build locally: `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o wings_linux_amd64 .` - tests: `go test ./...`

---

What follows is upstream's README.

# Pelican Wings

Wings is Pelican's server control plane, built for the rapidly changing gaming industry and designed to be
highly performant and secure. Wings provides an HTTP API allowing you to interface directly with running server
instances, fetch server logs, generate backups, and control all aspects of the server lifecycle.

In addition, Wings ships with a built-in SFTP server allowing your system to remain free of Pelican specific
dependencies, and allowing users to authenticate with the same credentials they would normally use to access the Panel.

## Documentation

* [Panel Documentation](https://pelican.dev/docs/panel/getting-started)
* [Wings Documentation](https://pelican.dev/docs/wings/install)
* Or, get additional help [via Discord](https://discord.gg/pelican-panel)

## Reporting Issues

Feel free to report any wings specific issues or feature requests in [GitHub Issues](https://github.com/pelican/wings/issues/new).
