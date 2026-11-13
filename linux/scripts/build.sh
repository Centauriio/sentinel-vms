#!/bin/bash
# Builds release archives for Linux amd64 + arm64:
#   dist/sentinel-<version>-linux-<arch>.tar.gz  (sentinel, mediamtx, systemd unit, installer)
# and a Docker build context in dist/docker/ (Dockerfile + docker-compose.yml),
# and, when run on a Mac, for macOS arm64 + amd64 + universal:
#   dist/sentinel-<version>-macos-<arch>.zip     (sentinel, mediamtx, ffmpeg, cloudflared,
#                                                 ONNX Runtime, launchd installer; signed)
# Linux archives build on macOS and Linux (CI). SENTINEL_CACHE overrides the
# download cache dir. PLATFORMS="linux" or "macos" builds just one.
# macOS signing: DEVELOPER_ID_APPLICATION (else ad-hoc), see scripts/sign-macos.sh;
# NOTARY_PROFILE set = also notarize each zip.
#
#   VERSION=1.0.0 scripts/build.sh
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
VERSION="${VERSION:-$(git -C "$ROOT" describe --tags --always 2>/dev/null || echo dev)}"
MEDIAMTX_VERSION="v1.21.1"
# cloudflared (iPhone remote access). SHA-256s pinned from the release notes,
# which match GitHub's asset digests. Bump all three together.
CLOUDFLARED_VERSION="2026.10.0"
CLOUDFLARED_SHA256_amd64="d33ff2d14475178d2012c2c56beba87389ac5ded27649519f198a7d3134a99db"
CLOUDFLARED_SHA256_arm64="e6422b9d4f72d3194bc5a38676f13667c06666523217b842a877d72a80b5ac08"
# ffmpeg (motion detection + snapshots): BtbN LGPL static build, decode-only use.
# Pinned to a dated autobuild (the "latest" tag is rebuilt daily). SHA-256s are
# GitHub's asset digests. Bump all four together.
# BtbN deletes autobuilds after about two weeks, so an old pin 404s in CI
# (local builds keep working from the cache). Re-pin before tagging a release.
FFMPEG_RELEASE="autobuild-2026-10-20-13-08"
FFMPEG_BUILD="ffmpeg-n8.1.3-18-g46d899d7a0"
FFMPEG_SHA256_amd64="6369d7e2e647d3b467ba01b5779f211fb45acf5895a15514e0426f959d3563b4"
FFMPEG_SHA256_arm64="012b273562a8139b4d11151a5fe5d6051f09cd02259004a2d345921c69158597"
# Person/vehicle detection: ONNX Runtime (MIT; sha256 = GitHub asset digests)
# and YOLOX-Tiny (Apache-2.0; Megvii publishes no digest — pinned on first download).
ORT_VERSION="1.30.0"
ORT_SHA256_amd64="a5ed5a3cac51fbb2e90da632ae43d19212faaa20e76484e62bcb7c23ddb3b3fd"
ORT_SHA256_arm64="e16a27a8ed330bbc698df7330b0cf56e722f354e3bcc92118682c74ef3c3e3da"
YOLOX_URL="https://github.com/Megvii-BaseDetection/YOLOX/releases/download/0.1.1rc0/yolox_tiny.onnx"
YOLOX_SHA256="427cc366d34e27ff7a03e2899b5e3671425c262ea2291f88bb942bc1cc70b0f7"
# Models shipped in every archive (wave 16): "file url sha256", one per line.
# They match internal/models/catalog.go; keep the two in step. CLIP (600 MB)
# stays a download from the Models page.
MODELS="
yolox_s.onnx https://github.com/Megvii-BaseDetection/YOLOX/releases/download/0.1.1rc0/yolox_s.onnx c5c2d13e59ae883e6af3b45daea64af4833a4951c92d116ec270d9ddbe998063
face_det.onnx https://github.com/opencv/opencv_zoo/raw/48b10fa505aeb51fbd127fc60a5c660040e823b7/models/face_detection_yunet/face_detection_yunet_2023mar.onnx 8f2383e4dd3cfbb4553ea8718107fc0423210dc964f9f4280604804ed2552fa4
face_rec.onnx https://github.com/opencv/opencv_zoo/raw/48b10fa505aeb51fbd127fc60a5c660040e823b7/models/face_recognition_sface/face_recognition_sface_2021dec.onnx 0ba9fbfa01b5270c96627c4ef784da859931e02f04419c829e83484087c34e79
plate_det.onnx https://www.modelscope.cn/models/RapidAI/RapidOCR/resolve/v3.10.0/onnx/PP-OCRv4/det/en_PP-OCRv3_det_mobile.onnx ea07c15d38ac40cd69da3c493444ec75b44ff23840553ff8ba102c1219ed39c2
plate_rec.onnx https://www.modelscope.cn/models/RapidAI/RapidOCR/resolve/v3.10.0/onnx/PP-OCRv4/rec/en_PP-OCRv4_rec_mobile.onnx e8770c967605983d1570cdf5352041dfb68fa0c21664f49f47b155abd3e0e318
plate_rec.txt https://www.modelscope.cn/models/RapidAI/RapidOCR/resolve/v3.10.0/paddle/PP-OCRv4/rec/en_PP-OCRv4_rec_mobile/en_dict.txt 5662df9d2d03f0e8ca0d3b0649d6acbab904b6a14b3d3521463c71c37c668ce3
audio.onnx https://huggingface.co/audiomagic/yamnet-onnx/resolve/main/yamnet.onnx d3835ffbbd4a1bb3e777f0ca217b5007907f5171dd5d17c4236b95b2af8f908e
"
# stage_models <stage dir>: copies every shipped model into <stage>/models.
stage_models() {
    install -m 0644 "$CACHE/yolox_tiny.onnx" "$1/models/yolox_tiny.onnx"
    echo "$MODELS" | while read -r f _ _; do
        if [ -n "$f" ]; then install -m 0644 "$CACHE/$f" "$1/models/$f"; fi
    done
}
# macOS. MediaMTX: same version, checked against its checksums.sha256 like
# Linux. cloudflared: GitHub asset digests of the same release. ONNX Runtime:
# Microsoft ships macOS builds for Apple silicon only since 1.24, so Intel
# Macs get 1.23.2, the last x86_64 release (Sentinel accepts ORT API >= 17).
# ffmpeg: built from source as LGPL by scripts/build-ffmpeg-macos.sh.
CLOUDFLARED_DARWIN_SHA256_amd64="903845b81828c8cb3c5d13d816a2de71c06a3da5785469df8eb0e1b736d92f9f"
CLOUDFLARED_DARWIN_SHA256_arm64="a2f79ff7b9420aa537d74af239f376da170bbabeb529aec416002adac6a72e70"
ORT_DARWIN_VERSION_arm64="$ORT_VERSION"
ORT_DARWIN_SHA256_arm64="6ebb5062a934537c352937821f9fe9718e7de1a2db1122a93dd363ffd53a7012"
ORT_DARWIN_VERSION_amd64="1.23.2"
ORT_DARWIN_SHA256_amd64="d10359e16347b57d9959f7e80a225a5b4a66ed7d7e007274a15cae86836485a6"
PLATFORMS="${PLATFORMS:-linux macos}"

verify() { # file expected-sha256
    if [ "$(shasum -a 256 "$1" | cut -d' ' -f1)" != "$2" ]; then
        echo "ERROR: checksum mismatch for $1" >&2
        rm -f "$1"
        exit 1
    fi
}
DIST="$ROOT/dist"
CACHE="${SENTINEL_CACHE:-$ROOT/.cache}"
export PATH="$HOME/.local/go/bin:$PATH"

echo "==> Dashboard"
(cd "$ROOT/web" && npm ci --silent && npm run build --silent)

rm -rf "$DIST" && mkdir -p "$DIST/docker" "$CACHE"

[ -f "$CACHE/yolox_tiny.onnx" ] || curl -fsSL -o "$CACHE/yolox_tiny.onnx" "$YOLOX_URL"
verify "$CACHE/yolox_tiny.onnx" "$YOLOX_SHA256"
echo "==> Shipped models"
echo "$MODELS" | while read -r f url sha; do
    [ -n "$f" ] || continue
    [ -f "$CACHE/$f" ] || curl -fsSL -o "$CACHE/$f" "$url"
    verify "$CACHE/$f" "$sha"
done

echo "==> MediaMTX $MEDIAMTX_VERSION checksums"
SUMS="$CACHE/mediamtx-$MEDIAMTX_VERSION.sha256"
[ -f "$SUMS" ] || curl -fsSL -o "$SUMS" "https://github.com/bluenviron/mediamtx/releases/download/$MEDIAMTX_VERSION/checksums.sha256"

LINUX_ARCHES="amd64 arm64"
case " $PLATFORMS " in *" linux "*) ;; *) LINUX_ARCHES="" ;; esac
for ARCH in $LINUX_ARCHES; do
    echo "==> linux/$ARCH"
    STAGE="$DIST/sentinel-$VERSION-linux-$ARCH"
    mkdir -p "$STAGE"
    (cd "$ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH=$ARCH go build -trimpath \
        -ldflags "-s -w -X main.Version=$VERSION" -o "$STAGE/sentinel" ./cmd/sentinel)

    TARBALL="mediamtx_${MEDIAMTX_VERSION}_linux_${ARCH}.tar.gz"
    [ -f "$CACHE/$TARBALL" ] || curl -fsSL -o "$CACHE/$TARBALL" \
        "https://github.com/bluenviron/mediamtx/releases/download/$MEDIAMTX_VERSION/$TARBALL"
    # Lines are "<sha256> *<file>" (sha256sum binary mode) or "<sha256>  <file>".
    EXPECTED="$(awk -v f="$TARBALL" '{ n = $2; sub(/^\*/, "", n); if (n == f) print $1 }' "$SUMS")"
    ACTUAL="$(shasum -a 256 "$CACHE/$TARBALL" | cut -d' ' -f1)"
    if [ -z "$EXPECTED" ] || [ "$EXPECTED" != "$ACTUAL" ]; then
        echo "ERROR: checksum mismatch for $TARBALL" >&2
        exit 1
    fi
    tar -xzf "$CACHE/$TARBALL" -C "$STAGE" mediamtx

    CF="$CACHE/cloudflared-$CLOUDFLARED_VERSION-linux-$ARCH"
    [ -f "$CF" ] || curl -fsSL -o "$CF" \
        "https://github.com/cloudflare/cloudflared/releases/download/$CLOUDFLARED_VERSION/cloudflared-linux-$ARCH"
    CF_EXPECTED_VAR="CLOUDFLARED_SHA256_$ARCH"
    if [ "$(shasum -a 256 "$CF" | cut -d' ' -f1)" != "${!CF_EXPECTED_VAR}" ]; then
        echo "ERROR: checksum mismatch for cloudflared-linux-$ARCH" >&2
        rm -f "$CF"
        exit 1
    fi
    install -m 0755 "$CF" "$STAGE/cloudflared"

    case "$ARCH" in amd64) FFARCH=linux64 ;; arm64) FFARCH=linuxarm64 ;; esac
    FFTAR="$FFMPEG_BUILD-$FFARCH-lgpl-8.1.tar.xz"
    [ -f "$CACHE/$FFTAR" ] || curl -fsSL -o "$CACHE/$FFTAR" \
        "https://github.com/BtbN/FFmpeg-Builds/releases/download/$FFMPEG_RELEASE/$FFTAR"
    FF_EXPECTED_VAR="FFMPEG_SHA256_$ARCH"
    if [ "$(shasum -a 256 "$CACHE/$FFTAR" | cut -d' ' -f1)" != "${!FF_EXPECTED_VAR}" ]; then
        echo "ERROR: checksum mismatch for $FFTAR" >&2
        rm -f "$CACHE/$FFTAR"
        exit 1
    fi
    tar -xJf "$CACHE/$FFTAR" -C "$STAGE" --strip-components=2 "${FFTAR%.tar.xz}/bin/ffmpeg"
    tar -xJf "$CACHE/$FFTAR" -C "$STAGE" --strip-components=1 "${FFTAR%.tar.xz}/LICENSE.txt"
    mv "$STAGE/LICENSE.txt" "$STAGE/ffmpeg-LICENSE.txt"

    case "$ARCH" in amd64) ORTARCH=x64 ;; arm64) ORTARCH=aarch64 ;; esac
    ORTTAR="onnxruntime-linux-$ORTARCH-$ORT_VERSION.tgz"
    [ -f "$CACHE/$ORTTAR" ] || curl -fsSL -o "$CACHE/$ORTTAR" \
        "https://github.com/microsoft/onnxruntime/releases/download/v$ORT_VERSION/$ORTTAR"
    ORT_EXPECTED_VAR="ORT_SHA256_$ARCH"
    verify "$CACHE/$ORTTAR" "${!ORT_EXPECTED_VAR}"
    mkdir -p "$STAGE/lib" "$STAGE/models"
    tar -xzf "$CACHE/$ORTTAR" -C "$STAGE/lib" --strip-components=2 "${ORTTAR%.tgz}/lib/libonnxruntime.so.$ORT_VERSION"
    mv "$STAGE/lib/libonnxruntime.so.$ORT_VERSION" "$STAGE/lib/libonnxruntime.so"
    tar -xzf "$CACHE/$ORTTAR" -C "$STAGE" --strip-components=1 "${ORTTAR%.tgz}/LICENSE"
    mv "$STAGE/LICENSE" "$STAGE/onnxruntime-LICENSE.txt"
    stage_models "$STAGE"
    cat > "$STAGE/THIRD-PARTY.txt" <<NOTICE
Sentinel VMS for Linux bundles these unmodified third-party programs:

- MediaMTX $MEDIAMTX_VERSION (MIT) — https://github.com/bluenviron/mediamtx
- cloudflared $CLOUDFLARED_VERSION (Apache-2.0) — https://github.com/cloudflare/cloudflared
- ONNX Runtime $ORT_VERSION (MIT) — https://github.com/microsoft/onnxruntime — license in onnxruntime-LICENSE.txt
- YOLOX-Tiny object detection model (Apache-2.0, Megvii) — https://github.com/Megvii-BaseDetection/YOLOX
- FFmpeg $FFMPEG_BUILD, LGPL-2.1-or-later build by BtbN — license in ffmpeg-LICENSE.txt.
  Binary: https://github.com/BtbN/FFmpeg-Builds/releases/tag/$FFMPEG_RELEASE
  Source: https://github.com/FFmpeg/FFmpeg (build scripts: https://github.com/BtbN/FFmpeg-Builds)
NOTICE

    cp "$ROOT/packaging/sentinel.service" "$ROOT/packaging/install.sh" "$ROOT/packaging/docker-compose.yml" "$STAGE/"
    cp -R "$ROOT/packaging/usb" "$STAGE/usb"
    # Tests and interpreter caches are development-only.
    rm -rf "$STAGE/usb/tests" "$STAGE/usb/__pycache__"
    # --no-xattrs/COPYFILE_DISABLE: keep macOS metadata out, or GNU tar on Linux warns on extract.
    # (bsdtar only; GNU tar on Linux CI has no --no-mac-metadata and nothing to strip.)
    TAR_FLAGS=()
    if tar --version 2>/dev/null | grep -q bsdtar; then TAR_FLAGS=(--no-xattrs --no-mac-metadata); fi
    COPYFILE_DISABLE=1 tar ${TAR_FLAGS[@]+"${TAR_FLAGS[@]}"} -czf "$STAGE.tar.gz" -C "$DIST" "$(basename "$STAGE")"

    mkdir -p "$DIST/docker/linux-$ARCH/licenses"
    cp -R "$STAGE/sentinel" "$STAGE/mediamtx" "$STAGE/cloudflared" "$STAGE/ffmpeg" "$STAGE/lib" "$STAGE/models" "$DIST/docker/linux-$ARCH/"
    cp "$STAGE/THIRD-PARTY.txt" "$STAGE/ffmpeg-LICENSE.txt" "$STAGE/onnxruntime-LICENSE.txt" "$DIST/docker/linux-$ARCH/licenses/"
done
if [ -n "$LINUX_ARCHES" ]; then
    cp "$ROOT/packaging/Dockerfile" "$ROOT/packaging/docker-compose.yml" "$DIST/docker/"
    # Empty /data owned by nonroot in the image (see Dockerfile).
    mkdir -p "$DIST/docker/rootfs/data" && chmod 0750 "$DIST/docker/rootfs/data"
fi

# ---------------------------------------------------------------------------
# macOS: needs a Mac with the Xcode command line tools (clang for ffmpeg,
# lipo, codesign, ditto).
# ---------------------------------------------------------------------------
MACOS_BUILT=()
case " $PLATFORMS " in *" macos "*) MACOS_WANTED=1 ;; *) MACOS_WANTED=0 ;; esac
if [ "$MACOS_WANTED" = 1 ] && [ "$(uname -s)" != Darwin ]; then
    echo "==> Skipping macOS archives (they need a Mac: clang, lipo, codesign)"
    MACOS_WANTED=0
fi
if [ "$MACOS_WANTED" = 1 ]; then
    for ARCH in arm64 amd64; do
        echo "==> macos/$ARCH"
        STAGE="$DIST/sentinel-$VERSION-macos-$ARCH"
        mkdir -p "$STAGE/lib" "$STAGE/models"
        (cd "$ROOT" && CGO_ENABLED=0 GOOS=darwin GOARCH=$ARCH go build -trimpath \
            -ldflags "-s -w -X main.Version=$VERSION" -o "$STAGE/sentinel" ./cmd/sentinel)

        TARBALL="mediamtx_${MEDIAMTX_VERSION}_darwin_${ARCH}.tar.gz"
        [ -f "$CACHE/$TARBALL" ] || curl -fsSL -o "$CACHE/$TARBALL" \
            "https://github.com/bluenviron/mediamtx/releases/download/$MEDIAMTX_VERSION/$TARBALL"
        EXPECTED="$(awk -v f="$TARBALL" '{ n = $2; sub(/^\*/, "", n); if (n == f) print $1 }' "$SUMS")"
        if [ -z "$EXPECTED" ]; then echo "ERROR: no checksum for $TARBALL" >&2; exit 1; fi
        verify "$CACHE/$TARBALL" "$EXPECTED"
        tar -xzf "$CACHE/$TARBALL" -C "$STAGE" mediamtx

        CFTGZ="cloudflared-$CLOUDFLARED_VERSION-darwin-$ARCH.tgz"
        [ -f "$CACHE/$CFTGZ" ] || curl -fsSL -o "$CACHE/$CFTGZ" \
            "https://github.com/cloudflare/cloudflared/releases/download/$CLOUDFLARED_VERSION/cloudflared-darwin-$ARCH.tgz"
        CF_EXPECTED_VAR="CLOUDFLARED_DARWIN_SHA256_$ARCH"
        verify "$CACHE/$CFTGZ" "${!CF_EXPECTED_VAR}"
        tar -xzf "$CACHE/$CFTGZ" -C "$STAGE" cloudflared

        "$ROOT/scripts/build-ffmpeg-macos.sh" "$ARCH" "$STAGE"

        ORTV_VAR="ORT_DARWIN_VERSION_$ARCH"; ORTV="${!ORTV_VAR}"
        case "$ARCH" in arm64) ORTARCH=arm64 ;; amd64) ORTARCH=x86_64 ;; esac
        ORTTAR="onnxruntime-osx-$ORTARCH-$ORTV.tgz"
        [ -f "$CACHE/$ORTTAR" ] || curl -fsSL -o "$CACHE/$ORTTAR" \
            "https://github.com/microsoft/onnxruntime/releases/download/v$ORTV/$ORTTAR"
        ORT_EXPECTED_VAR="ORT_DARWIN_SHA256_$ARCH"
        verify "$CACHE/$ORTTAR" "${!ORT_EXPECTED_VAR}"
        ORTX="$(mktemp -d)"
        tar -xzf "$CACHE/$ORTTAR" -C "$ORTX"
        install -m 0755 "$ORTX"/*/lib/libonnxruntime.$ORTV.dylib "$STAGE/lib/libonnxruntime.dylib"
        install -m 0644 "$ORTX"/*/LICENSE "$STAGE/onnxruntime-LICENSE.txt"
        rm -rf "$ORTX"
        stage_models "$STAGE"
        install -m 0755 "$ROOT/packaging/macos/install.sh" "$ROOT/packaging/macos/uninstall.sh" "$STAGE/"
        MACOS_BUILT+=("$STAGE")
    done

    echo "==> macos/universal"
    U="$DIST/sentinel-$VERSION-macos-universal"
    A="$DIST/sentinel-$VERSION-macos-arm64" X="$DIST/sentinel-$VERSION-macos-amd64"
    mkdir -p "$U/lib" "$U/models"
    for f in sentinel mediamtx cloudflared ffmpeg lib/libonnxruntime.dylib; do
        lipo -create "$A/$f" "$X/$f" -output "$U/$f"
    done
    cp "$A"/models/* "$U/models/"
    cp "$A/ffmpeg-LICENSE.txt" "$A/onnxruntime-LICENSE.txt" "$A/install.sh" "$A/uninstall.sh" "$U/"
    MACOS_BUILT+=("$U")

    for STAGE in "${MACOS_BUILT[@]}"; do
        case "$STAGE" in
        *-arm64) ORT_NOTE="$ORT_DARWIN_VERSION_arm64 (Apple silicon)" ;;
        *-amd64) ORT_NOTE="$ORT_DARWIN_VERSION_amd64 (Intel; the last release with an x86_64 macOS build)" ;;
        *) ORT_NOTE="$ORT_DARWIN_VERSION_arm64 (Apple silicon slice) and $ORT_DARWIN_VERSION_amd64 (Intel slice)" ;;
        esac
        cat >"$STAGE/THIRD-PARTY.txt" <<NOTICE
Sentinel VMS for macOS bundles these third-party programs:

- MediaMTX $MEDIAMTX_VERSION (MIT) — https://github.com/bluenviron/mediamtx
- cloudflared $CLOUDFLARED_VERSION (Apache-2.0) — https://github.com/cloudflare/cloudflared
- ONNX Runtime $ORT_NOTE (MIT) — https://github.com/microsoft/onnxruntime — license in onnxruntime-LICENSE.txt
- YOLOX-Tiny object detection model (Apache-2.0, Megvii) — https://github.com/Megvii-BaseDetection/YOLOX
- FFmpeg $(sed -n 's/^FFMPEG_VERSION="\(.*\)"/\1/p' "$ROOT/scripts/build-ffmpeg-macos.sh"), LGPL-2.1-or-later, built unmodified from the
  official source release (https://ffmpeg.org/releases/) with only LGPL components and
  Apple system frameworks — license and exact configure flags in ffmpeg-LICENSE.txt.
  Source: https://ffmpeg.org/download.html (build script: scripts/build-ffmpeg-macos.sh)

All executables are re-signed by Handoffgrid LLC except cloudflared, which keeps
Cloudflare's own Developer ID signature.
NOTICE
        "$ROOT/scripts/sign-macos.sh" ${NOTARY_PROFILE:+--notarize} "$STAGE"
        (cd "$DIST" && ditto -c -k --sequesterRsrc --keepParent "$(basename "$STAGE")" "$(basename "$STAGE").zip")
    done
fi

echo
echo "Done:"
ls -lh "$DIST"/*.tar.gz "$DIST"/*.zip 2>/dev/null || true
if [ -n "$LINUX_ARCHES" ]; then
    echo "Docker: docker buildx build --platform linux/amd64,linux/arm64 --build-arg VERSION=$VERSION -t sentinel:$VERSION \"$DIST/docker\""
fi
