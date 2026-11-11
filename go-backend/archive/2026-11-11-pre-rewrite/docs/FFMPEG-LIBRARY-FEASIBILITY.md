# FFmpeg/ffprobe libraries and a single executable

Analysis date: 2026-11-09. Review result: **REVISE before implementation**.
Scope: current Go production call sites, build packaging and upstream library
documentation. This is feasibility research, not a dependency approval or an
implemented/benchmarked migration. Existing unrelated work and index conflicts
are outside this review.

## Conclusion

A native single executable is technically feasible by statically linking
FFmpeg's C libraries through CGO. That removes the external media executables
and shared FFmpeg libraries if the complete dependency graph is linked
statically. It replaces today's `CGO_ENABLED=0` build and requires substantial
media orchestration code; a Go wrapper does not reproduce the CLI automatically.
Linux amd64/arm64 are sensible first proof targets. Do not claim one portable
binary across operating systems or architectures, or fully static macOS builds.

For a CGO-free single executable, embedded WebAssembly is a plausible second
route, but full compatibility and adequate audiobook throughput are unproven.
A collection of pure-Go tag/container packages is not an established replacement
for the full decode/encode/remux/probe feature set found here.

Recommendation: retain subprocesses for now. If single-executable delivery is
a firm product requirement, first evaluate a pinned static libav build and a
bounded probe + HLS proof. Evaluate WASM instead if retaining CGO-free builds is
also a firm requirement. New libraries and build-policy changes need a separate
owner decision; this analysis approves neither.

## Observed usage and compatibility matrix

All production process creation is centralized in
[`mediatools.go`](../internal/mediatools/mediatools.go), through
`exec.CommandContext`. There are six operations (typed probing has two callers):

| Operation and current owner | Library replacement | Difficulty / preserved contract |
|---|---|---|
| Typed probe: `scanner/files.go:546`, `podcasts/downloads.go:293` | libavformat discovery, codec parameters, dictionaries and chapters | Medium. Duration, codec, bitrate, language, stream selection, tag precedence and chapter time bases feed persisted metadata. Scanner and podcast precedence differ; preserve both. |
| Full probe: `catalog/item_media.go:161`, `mediatools.ProbeRaw` | Explicit report projection over libav state | High. Admin API exposes format/streams/programs/chapters/private data. Returning only typed `Info` loses functionality. ffprobe's JSON writer is tool code, not a turnkey libavformat API. |
| Cover resize: `catalog/cover.go:144` | Go image decode/scale/encode, or libavcodec + swscale | Low to medium for common images; reconcile all accepted inputs. WebP/JPEG/PNG output, dimensions/aspect, MIME and cache paths remain. |
| Metadata embedding: `audiotools/tools.go:375` | Demux/remux, metadata and chapter mapping, attached picture packets | High. Keep copied audio, metadata precedence, chapter mapping, backups, temporary output/rename, queue/cancel and per-track events. MP3/MP4/Ogg/FLAC and fallback Matroska muxers exist. |
| M4B merge: `audiotools/tools.go:443` | Concat/demux, decode/resample/encode, MP4 mux | High. AAC/stereo/128k defaults, request-controlled codec/channels/bitrate, chapters, MJPEG cover, source backups and installation behavior. |
| HLS: `playback/hls.go:167` | libavformat concat + HLS/MPEG-TS mux, codec copy or AAC pipeline | Highest risk. Six-second segments, timestamp policy, numbered seek restarts, playlist ownership, cancellation and cleanup. |

The bundled client actively exposes full probe output in
`client/components/modals/AudioFileDataModal.vue`; manual conversion and
embedding are also retained product functionality. Do not remove them to make
the packaging goal easier. Direct playback already serves files without FFmpeg,
but that does not cover scanning, images, conversion or transcoded playback.

Notable invariants:

- Scanner prefers a default audio stream, uses the first video stream as the
  embedded-cover indicator, prefers stream bitrate, and sorts/reindexes chapters.
  Podcast probing instead takes the first audio stream and prefers container
  bitrate. A shared probe must leave these consumer policies intact.
- Discovery recognizes MP3/WAV/MP4-family/Ogg/Opus/AAC/FLAC/WMA/AIFF/WebM/
  Matroska/AMR-WB/CAF/MPEG (`internal/filekind/filekind.go`). An allowlist of
  convenient codecs would narrow current behavior.
- HLS copies audio by default; only selected MIME/codec combinations encode AAC.
  Re-encoding every stream changes behavior and cost.
- Process cancellation currently kills an isolated process; HLS waits before
  restart/cleanup. Context cancellation alone cannot kill arbitrary C code.
  Native bindings need interrupt callbacks, cooperative packet/frame loops and
  demonstrated bounded shutdown. Native codec faults would share server memory.
- `FFMPEG_PATH` and `FFPROBE_PATH` are operational overrides today. Their meaning
  and availability checks need an explicit decision if executables disappear.

## Options

| Approach | Single executable with no media install? | Assessment |
|---|---|---|
| `ffmpeg-go` and similar command builders | No | Still executes ffmpeg. Existing runner is already small; adds no packaging benefit. |
| CGO bindings + dynamic libav | No | Removes CLI processes but requires shared libraries. |
| CGO bindings + static libav and external codecs | Yes, per target, subject to link verification | Most credible native-performance path. More complex toolchains and application code. |
| Embedded WASM libav + pure-Go runtime | Potentially yes, retaining CGO-free builds | Promising, but format coverage, metadata/chapters, cancellation, resources and long-file throughput need proof. |
| Pure-Go format/codec packages | Partial | Useful for isolated images or metadata; no complete replacement established here. |
| Embed static ffmpeg/ffprobe, extract and execute | One download; multiple runtime executables | Packaging alternative preserving process isolation and CLI behavior; requires executable extraction/storage. It is not a library migration. |

### Native candidate

[`go-astiav`](https://github.com/asticode/go-astiav) provides actual C bindings,
examples for probing/remuxing/transcoding, and explicit allocation/free APIs.
Its current README targets FFmpeg n9.0 and warns of breaking changes without
Go major-version path changes: pin a tested commit and matching FFmpeg release.
This is a candidate, not a maintenance audit or validated static build.

[FFmpeg's libraries](https://ffmpeg.org/doxygen/trunk/md_README.html) provide
containers/codecs, filters, scaling and resampling. Its
[transcoding example](https://ffmpeg.org/doxygen/trunk/transcoding_8c-example.html)
illustrates the pipeline work the CLI currently performs for us. Required
libraries likely include avformat, avcodec, avutil, swresample and swscale;
avfilter is needed only if the chosen pipeline uses filter graphs.

Static linking must include enabled external codec/image dependencies too.
Today's Dockerfile cross-compiles Go without a target C compiler; CGO needs
per-target headers, static archives, compiler/linker and reproducible build
configuration. Pin codec features, track source/build provenance and satisfy
the licenses of the exact combined build; do not assume a binding's license
covers FFmpeg and enabled codecs. No binary-size, speedup or LOC saving was
measured. Removing process startup may help frequent probes/covers; it does
not establish a meaningful long-transcode speedup. Boundaries and pipeline
code are likely to increase handwritten code compared with the current runner.

### CGO-free candidate

[`ffmpeg-wasi`](https://ffmpeg-wasi.phpboyscout.uk/) documents an embeddable
libav engine using wazero and a structured job API, rather than CLI flags.
Its [intermediate container profile](https://ffmpeg-wasi.phpboyscout.uk/reference/containers/)
includes HLS/MPEG-TS and concat. This is closer to the workload than a metadata
reader, but published capability does not prove this application's contracts.

The [codec matrix](https://ffmpeg-wasi.phpboyscout.uk/reference/codecs/) includes
AAC and intermediate WebP encoding, but omits AMR. Its
[limits](https://ffmpeg-wasi.phpboyscout.uk/reference/limits/) document
single-threaded WASM without native assembly, fixed job caps, mounted-filesystem
requirements, and host responsibility for resource budgets. Concat lists avoid
treating the input-entry cap as a simple maximum number of book tracks; verify
the actual adapter. HLS's presence does not establish incremental segment
visibility, seek/reset behavior or chapter/artwork preservation. Published video
slowdown figures are not audiobook benchmarks and are not used as estimates.
The faster external native driver would reintroduce a subprocess.

For covers alone, stdlib JPEG/PNG plus scaling and a CGO-free WebP codec are
viable candidates. [`gen2brain/webp`](https://github.com/gen2brain/webp) currently
documents a transpiled-Go fallback and `nodynamic` to disable shared-library
lookup. Validate a pinned release rather than assuming its historical WASM
implementation. Full media migration would still remain.

## Proposed proof and decision gates (not dispatched)

1. Decide whether CGO is acceptable and whether “single binary” means one
   download or no subprocess/shared-library runtime dependencies. Choose initial
   target platforms and preserve the full feature set above.
2. After owner approval of the candidate, use the existing mediatools boundary
   for typed probing first. Avoid a generic media framework or a CLI-argument
   translator. Keep lifecycle and persistence orchestration in current owners.
3. In a disposable proof, run representative long AAC-copy and FLAC/Opus-to-AAC
   HLS journeys, multi-file joins, backward/forward seeks and cancel/close.
   Compare startup latency, throughput, peak total RSS, concurrent sessions,
   output timeline and cleanup. Establish an explicit acceptable regression
   budget before promoting it. For WASM also prove bounded memory and file I/O.
4. Cover the entire input-format matrix, chapters/time bases/default streams,
   Unicode tags and conflicting metadata, corrupt/truncated files, and full
   probe JSON value/types. Existing scan, podcast, cover, audio-tool and playback
   tests are useful seeds, not proof of unimplemented adapters.
5. Verify remuxed audio preservation, artwork/chapters, cancellation without
   source replacement, queue/events/backups and TS rollback of Go-written
   metadata. Retain HTTP MIME/cache/range semantics; encoded image bytes need
   not match upstream.
6. Build Linux amd64/arm64 artifacts; inspect interpreter/NEEDED entries and run
   with no ffmpeg/ffprobe or libav shared libraries installed. No auto-download
   fallback. Embed the WASM module if choosing WASM. Pin release sources,
   hashes, build flags, licenses and update procedure.

Removing FFmpeg alone does **not** make the complete current app self-contained:
`Dockerfile.go` copies `client/dist` separately, and `server/runtime.go` serves
`Config.ClientPath`. A complete one-file app also needs a separately scoped
embedded-client decision. Configuration, metadata and media remain writable
external data; time-zone/certificate needs depend on the chosen runtime target.

## Validation and continuation

Source inventory and upstream primary documentation reviewed; no candidate was
installed, built or benchmarked, and runtime tests were not run for this prose
packet. Existing master/index conflicts and Wave G work remain untouched.
Next action is an owner choice of packaging/build constraints, followed by a
bounded proof if authorized; no implementation card is marked complete.

Documentation correction discovered: current cover resizing calls
`mediatools.Run`, so it honors `FFMPEG_PATH`. TESTING.md's contrary statement was
stale; matching ffprobe on PATH remains useful for tests that invoke it directly.
