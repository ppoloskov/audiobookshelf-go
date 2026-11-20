# Local acceptance environment

Authoritative entrypoint: `make verify` (or `make check` / `make oracle`) from
repository root after `make upstream`; see
[testmachine/oracle/README.md](testmachine/oracle/README.md). The commands below
configure prerequisites or diagnose individual components; they are not
alternative whole-contract acceptance gates.

Use the Go version declared by go.mod. On this host validation used installed Go
1.27.0 with GOTOOLCHAIN=local, without changing the suggested 1.27.3 toolchain.

## Reproducible Nix environment (recommended)

[flake.nix](flake.nix) provides the full build, lint, and oracle environment on
macOS (Apple Silicon and Intel) and Linux (`x86_64-linux`, `aarch64-linux`)
with Go, Node 22, WebP-enabled `ffmpeg`/`ffprobe`, `golangci-lint`, `gnumake`,
`git`, `python3`, and `sqlite` — without needing `mise` or custom FFmpeg
builds.

```sh
nix develop              # or: make nix-shell
make upstream            # once, to fetch pinned TS v2.37.2 into .oracle-upstream/
make verify              # run Twin-Server Differential Oracle
```

Or run single commands from outside the shell:

```sh
make nix-test            # go test ./... -count=1 inside nix develop
make nix-verify          # make verify inside nix develop
```

For automatic shell activation with `direnv`:

```sh
cp .envrc.example .envrc
direnv allow
```

Inside `nix develop`, `shellHook` exports `ORACLE_NODE_CMD=node`, `FFMPEG_PATH`,
and `FFPROBE_PATH` so `make verify`, `make oracle`, `make openapi`, and Go
tests automatically use the Nix-provided Node 22 and WebP-enabled FFmpeg.

## Shipping source

The canonical development checkout is master. For acceptance use the committed
clean source, or a clean `git archive HEAD` plus exactly the approved working
changes. Record the base SHA and overlay list. Do not report a dirty-workspace
failure as a shipping regression.

## FFmpeg with WebP

Cover conversion and audio tests require an `ffmpeg` / `ffprobe` build with
`libwebp`, `libmp3lame`, `aac`, and `libopus`. **Use `nix develop`** (see above)
to get a pinned, prebuilt WebP-enabled FFmpeg on `PATH` with `FFMPEG_PATH` and
`FFPROBE_PATH` configured automatically — no manual FFmpeg compilation or
temporary prefixes needed.

To verify your `ffmpeg` supports WebP encoding:

```sh
ffmpeg -hide_banner -encoders 2>/dev/null | grep -i webp
```
source tree; source version/hash/flags above remain durable when logs expire.

## Targeted oracle and unit checks

From the repository root, with `.oracle-upstream/` populated via
`make upstream`:

```sh
make oracle-unit
make oracle ORACLE_ARGS="-run TestOracleFlows/playback"
```

This exercises the pure-Go differ unit tests (`make oracle-unit`) or a filtered
live twin-server oracle flow (requiring real FFmpeg, disposable fixtures, and
loopback access).
