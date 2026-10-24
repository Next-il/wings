# Upstream

NextHost Wings started from Pelican Wings and is maintained here as its own project. Nothing is rebased
automatically: upstream changes are read and ported by hand, security fixes first.

Upstream: https://github.com/pelican-dev/wings (git remote `upstream`)
Base: pelican-dev/wings v1.0.0-rc.1 (7b489da7b8196d0b4baef3e2db062c06d743e2f9)
Last reviewed: 7b489da7b8196d0b4baef3e2db062c06d743e2f9 (v1.0.0-rc.1), 2026-10-24

## Reviewing upstream

1. `git fetch upstream` and read what is new: `git log --oneline <last reviewed>..upstream/main`, plus
   https://github.com/pelican-dev/wings/releases and the security advisories
   (https://github.com/pelican-dev/wings/security/advisories).
2. Port what we want into a branch - security fixes always, within days of an advisory; features when they
   help NextHost. `git cherry-pick -x <sha>` when it applies cleanly, by hand otherwise, naming the upstream
   commit in the message.
3. Update "Last reviewed" above to the newest upstream commit you read, even when nothing was taken, so the
   next review starts there. Move "Base" only when a port brings in a whole upstream release.
4. Note what was ported in CHANGELOG-NEXTHOST.md under the next release.

## Versions

Our tags are the upstream base plus our own build number: `v1.0.0-rc.1.nh.1`, `v1.0.0-rc.1.nh.2`, ... and
`v1.0.0.nh.1` once the base moves to upstream 1.0.0. The node Doctor parses this form, and the pinned
version lives in NextHost's config (`WINGS_PINNED_VERSION`).

The Go module path stays `github.com/pelican/wings` so upstream commits port without rewriting imports.
