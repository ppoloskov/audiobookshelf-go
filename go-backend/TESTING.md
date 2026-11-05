# Local acceptance environment

Use the Go version declared by go.mod. On this host validation used installed
Go 1.27.0 with GOTOOLCHAIN=local, without changing the suggested 1.27.3 toolchain.

## Shipping source and inherited work

The canonical development checkout is master. Inherited untracked
internal/identity/routes.go currently references absent httputil.NotFound;
including it can break builds and also contaminates source metrics. Preserve
it without staging, fixing or deleting it as part of unrelated work.

For acceptance use a clean `git archive HEAD` plus exactly the approved
working changes/new files, or the committed clean source. Record the base SHA
and overlay list. This is a validation snapshot, never a development branch.
Do not report a dirty-workspace failure as a shipping regression or a snapshot
pass as proof that inherited work compiles.

## Native FFmpeg with WebP

The host's Homebrew FFmpeg 9.0.2 lacks libwebp. A real isolated build now lives
at `/private/tmp/abs-cleanup-checkpoint-20261105/ffmpeg-9.0.2-webp`; global
Homebrew was unchanged. It passed a real WebP encode and all five image journey
cases formerly failing. Baseline c4cf90a9 full `go test ./... -count=1` passed.

From a clean shipping-source go-backend directory, with Go on PATH:

```sh
FFMPEG_TEST_PREFIX=/private/tmp/abs-cleanup-checkpoint-20261105/ffmpeg-9.0.2-webp
PATH="$FFMPEG_TEST_PREFIX/bin:$PATH" \
FFMPEG_PATH="$FFMPEG_TEST_PREFIX/bin/ffmpeg" \
GOTOOLCHAIN=local go test ./... -count=1
```

Both settings matter: playback/audiotools honor FFMPEG_PATH, while catalog
looks up `ffmpeg` on PATH. Keep ffprobe from the same prefix. Local httptest
listeners need loopback access; sandbox bind denial is an environment failure,
not a passing/skipped test. No cwebp wrapper, codec substitution or test skip.

The temporary prefix may disappear. Rebuild or supply a verified native binary
before claiming acceptance; `ffmpeg -h encoder=libwebp` alone can return zero
when the encoder is absent. Execute a real encode:

```sh
"$FFMPEG_TEST_PREFIX/bin/ffmpeg" -hide_banner -loglevel error \
  -f lavfi -i color=c=red:s=16x16:d=0.1 -frames:v 1 -c:v libwebp -f webp \
  -y "$FFMPEG_TEST_PREFIX/encoder-probe.webp"
"$FFMPEG_TEST_PREFIX/bin/ffprobe" -v error -show_streams \
  "$FFMPEG_TEST_PREFIX/encoder-probe.webp"
```

## Reproducing the isolated macOS build

Verified source: [FFmpeg 9.0.2](https://ffmpeg.org/releases/ffmpeg-9.0.2.tar.xz),
[detached signature](https://ffmpeg.org/releases/ffmpeg-9.0.2.tar.xz.asc),
[official key](https://ffmpeg.org/ffmpeg-devel.asc). Archive SHA-256:
`8c3850283eb25fa026482078a04051e0be17347b09ef81a0849bec15a96e002e`.
The good release signature matched fingerprint
`FCF986EA15E6E293A5644F10B4322F04D67658D8` published on the
[official download page](https://ffmpeg.org/download.html).

Existing Homebrew codec libraries were reused; none were installed/replaced.
Build sources, temporary pkg-config entry and logs stay under
`/private/tmp/abs-cleanup-checkpoint-20261105`. An isolated libmp3lame.pc names
the existing LAME library; explicit header/library flags resolve its
nonstandard metadata. Recreate the tested entry when rebuilding:

```sh
mkdir -p /private/tmp/abs-cleanup-checkpoint-20261105/pkgconfig
cat > /private/tmp/abs-cleanup-checkpoint-20261105/pkgconfig/libmp3lame.pc <<'PKGCONFIG'
prefix=/opt/homebrew/opt/lame
libdir=${prefix}/lib
includedir=${prefix}/include
Name: libmp3lame
Description: Homebrew LAME MP3 encoder
Version: 4.0
Libs: -L${libdir} -lmp3lame -lm
Cflags: -I${includedir}
PKGCONFIG
```

After signature/hash verification and unpacking, the
successful local configure/build/install commands were:

```sh
PKG_CONFIG_PATH=/private/tmp/abs-cleanup-checkpoint-20261105/pkgconfig:/opt/homebrew/opt/webp/lib/pkgconfig \
./configure \
  --prefix=/private/tmp/abs-cleanup-checkpoint-20261105/ffmpeg-9.0.2-webp \
  --enable-shared --enable-pthreads --enable-version3 --cc=clang \
  --extra-cflags=-I/opt/homebrew/opt/lame/include \
  --extra-ldflags=-L/opt/homebrew/opt/lame/lib \
  --enable-gpl --enable-libsvtav1 --enable-libopus --enable-libx264 \
  --enable-libmp3lame --enable-libdav1d --enable-libvmaf --enable-libvpx \
  --enable-libx265 --enable-openssl --enable-videotoolbox \
  --enable-audiotoolbox --enable-neon --enable-libwebp \
  --disable-ffplay --disable-doc --disable-debug
make -j4
make install
```

This records the tested host recipe, not a global package upgrade. Build,
install, signature, encode and test logs are retained beside the temporary
source tree; source version/hash/flags above remain durable when logs expire.
