# Global build argument for the optional ai and ai-intel stage base images.
ARG FEDORA_IMAGE=docker.io/library/fedora:44

# Web stage: builds the app's pages from this commit's source and lockfile,
# so an image from any commit always shows that commit's pages. It runs on
# the build machine for every target; Node never reaches the final image.
FROM --platform=$BUILDPLATFORM node:24.22.3-trixie-slim@sha256:af02d57b57719b9657a45199a79264a9437940cd55b3c7b3fc99615654f124a0 AS web
WORKDIR /src/app/frontend
COPY app/frontend/package.json app/frontend/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci --ignore-scripts --no-audit --no-fund
COPY app/frontend/ ./
# The language files stay plain here; the build stage packs them with bzip2.
RUN HOLYARRS_LANGUAGES_PLAIN=1 npm run build

# Build stage: runs on the build machine and cross-compiles for the target,
# so multi-arch builds (amd64, arm64, armv7) need no emulation.
FROM --platform=$BUILDPLATFORM golang:1.27.4@sha256:08bcb99a38f85a265f9a93d1d3323e2440eec333380f3ea26ee0dec9264bb41c AS build
ARG TARGETOS=linux
ARG TARGETARCH
ARG TARGETVARIANT
ARG VERSION
# Optional build-only resource controls; omitted values keep Go's defaults.
ARG GOMAXPROCS
ARG GOFLAGS
WORKDIR /src
COPY go.mod go.sum* ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
# The pages just built are what the program embeds; packing them before copying
# Go sources lets Go-only rebuilds reuse the packed frontend layer.
COPY --from=web /src/app/frontend/dist ./app/frontend/dist
COPY deploy/pack_languages.py ./deploy/pack_languages.py
# bzip2 is smaller than brotli for every language file (Python is part of the
# Go image; the program unpacks with Go's own bzip2 reader).
RUN python3 deploy/pack_languages.py app/frontend/dist
COPY app/frontend/embed.go ./app/frontend/embed.go
COPY app/backend/ ./app/backend/
COPY VERSION ./
# Inlining is off in HolyArrs's own packages and in the parts of Go's library
# and the sign-in libraries that are not speed-critical (about 1.3 MB less to
# download, same measured speed). The database, text handling, unpacking and
# database/sql keep it, where it matters for speed.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    set -eu; \
    ldflags="-s -w"; \
    v="${VERSION:-}"; \
    if [ -z "$v" ]; then \
        rel="$(tr -d '[:space:]' < VERSION)"; \
        v="${rel%.*}.$(( ${rel##*.} + 1 ))-dev"; \
    fi; \
    ldflags="$ldflags -X main.version=$v"; \
    noinline=""; \
    for p in github.com/HolyArrs/holyarrs/... \
        net/http/... crypto/tls/... crypto/x509/... net net/url net/textproto net/mail net/netip net/smtp mime/... \
        os os/exec os/signal os/user log/... encoding/asn1 encoding/pem encoding/csv archive/... vendor/... \
        io/fs path/filepath html/... text/... flag embed \
        github.com/go-jose/... github.com/coreos/... golang.org/x/oauth2/... github.com/spf13/... go4.org/... \
        github.com/google/... github.com/dustin/... github.com/hashicorp/...; do \
        noinline="$noinline -gcflags=$p=-l"; \
    done; \
    GOARM="${TARGETVARIANT#v}" CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath $noinline -ldflags="$ldflags" -o /out/holyarrs ./app/backend/cmd/holyarrs
# The folders the bare image needs: /config for the owner (1000:1000) and a
# /tmp anyone may write (Move's working copies, temporary files).
# They are copied through their parent folder, so each keeps its owner and
# mode on every builder: Podman (Buildah) leaves a folder named in COPY as
# 0755 root and ignores --chmod on it, and then SQLite finds no temporary
# folder ("disk I/O error (6410)").
RUN mkdir -p /out/dirs/config /out/dirs/tmp && chown 1000:1000 /out/dirs/config && chmod 1777 /out/dirs/tmp

# Runner stage, for the "ai" image only: the llama.cpp server that runs the
# built-in subtitle translator's model. It is the project's own release
# archive, pinned to one build and checked against its SHA-256. Only the
# server and the libraries it loads are kept. It is the Vulkan build, which
# runs on the CPU as well and on a graphics card when the container is given one.
FROM --platform=$BUILDPLATFORM golang:1.27.4@sha256:08bcb99a38f85a265f9a93d1d3323e2440eec333380f3ea26ee0dec9264bb41c AS llama
ARG TARGETARCH
# llama.cpp v1.0.0
ARG LLAMA_TAG=b12359
ARG LLAMA_SHA256_AMD64=19b60f00395a3f4954e46a5037695bc1d843404fc9d3124f82a612ecd8fbe9f0
ARG LLAMA_SHA256_ARM64=3f8ae812a58ac92e5a596ca9fbc988f52854d147c5a7fe1264dddc90c1d085b4
RUN set -eu; \
    case "$TARGETARCH" in \
        amd64) arch=x64; sum="$LLAMA_SHA256_AMD64" ;; \
        arm64) arch=arm64; sum="$LLAMA_SHA256_ARM64" ;; \
        *) echo "the ai image is built for amd64 and arm64 only" >&2; exit 1 ;; \
    esac; \
    curl -fsSL -o /tmp/llama.tar.gz "https://github.com/ggml-org/llama.cpp/releases/download/$LLAMA_TAG/llama-$LLAMA_TAG-bin-ubuntu-vulkan-$arch.tar.gz"; \
    echo "$sum  /tmp/llama.tar.gz" | sha256sum -c -; \
    mkdir -p /tmp/llama /out/llama; \
    tar -xzf /tmp/llama.tar.gz -C /tmp/llama --strip-components=1; \
    cp -a /tmp/llama/llama-server /tmp/llama/LICENSE /out/llama/; \
    cp -a /tmp/llama/*.so* /out/llama/; \
    rm -f /out/llama/lib*-bench-impl.so* /out/llama/libllama-completion-impl.so*; \
    chmod 0755 /out/llama/llama-server

# Speech stage, for the "ai" image only: whisper.cpp's command-line program,
# which makes a subtitle from a video's audio. The project publishes no Linux
# build, so it is compiled here from one pinned release, checked by its commit,
# with the Vulkan backend the model runner also uses.
FROM ${FEDORA_IMAGE} AS whisper
ARG WHISPER_TAG=v1.9.5
ARG WHISPER_COMMIT=d1be6fde11ac6e0407606b4e42fe72d34add8037
RUN --mount=type=cache,id=dnf-fedora44,target=/var/cache/libdnf5,sharing=locked \
    dnf install -y --setopt=install_weak_deps=False --setopt=keepcache=True \
        ca-certificates git gcc gcc-c++ make cmake vulkan-loader-devel glslc spirv-headers-devel findutils
RUN set -eu; \
    git clone --depth 1 --branch "$WHISPER_TAG" https://github.com/ggml-org/whisper.cpp /src; \
    cd /src; \
    test "$(git rev-parse HEAD)" = "$WHISPER_COMMIT"; \
    cmake -B build -DGGML_VULKAN=1 -DCMAKE_BUILD_TYPE=Release -DWHISPER_BUILD_TESTS=OFF \
        -DCMAKE_INSTALL_RPATH='$ORIGIN' -DCMAKE_BUILD_WITH_INSTALL_RPATH=ON; \
    cmake --build build -j "$(nproc)" --target whisper-cli whisper-server; \
    mkdir -p /out/whisper; \
    cp -a build/bin/whisper-cli build/bin/whisper-server /out/whisper/; \
    find build -name 'lib*.so*' -exec cp -a {} /out/whisper/ \; ; \
    cp LICENSE /out/whisper/LICENSE; \
    chmod 0755 /out/whisper/whisper-cli /out/whisper/whisper-server

# GPU stage (build with --target gpu): HolyArrs with full FFmpeg, VMAF models,
# and VAAPI/Vulkan/QSV video transcoding drivers (no AI runners).
FROM ${FEDORA_IMAGE} AS gpu
ARG TARGETARCH
ARG VERSION=dev
LABEL org.opencontainers.image.title="HolyArrs (with hardware video transcoding)" \
      org.opencontainers.image.source="https://github.com/HolyArrs/HolyArrs" \
      org.opencontainers.image.licenses="AGPL-3.0-only AND MIT AND GPL-3.0-or-later AND LGPL-2.1-or-later" \
      org.opencontainers.image.version="$VERSION"
RUN --mount=type=cache,id=dnf-fedora44,target=/var/cache/libdnf5,sharing=locked \
    set -eu; \
    dnf install -y --setopt=install_weak_deps=False --setopt=keepcache=True \
        "https://mirrors.rpmfusion.org/free/fedora/rpmfusion-free-release-$(rpm -E %fedora).noarch.rpm" \
        "https://mirrors.rpmfusion.org/nonfree/fedora/rpmfusion-nonfree-release-$(rpm -E %fedora).noarch.rpm"; \
    intel_pkgs=""; \
    case "${TARGETARCH:-$(uname -m)}" in \
        amd64|x86_64) intel_pkgs="intel-media-driver intel-vpl-gpu-rt" ;; \
    esac; \
    dnf install -y --allowerasing --setopt=install_weak_deps=False --setopt=keepcache=True \
        ca-certificates curl-minimal libva libva-utils mesa-va-drivers \
        vulkan-loader mesa-vulkan-drivers vmaf-models ffmpeg $intel_pkgs
COPY --from=build /out/holyarrs /holyarrs
COPY --from=build /out/dirs/ /
COPY LICENSE /LICENSE
ENV container=oci
EXPOSE 6262
VOLUME ["/config"]
HEALTHCHECK --interval=30s --timeout=10s --start-period=10s --retries=3 CMD ["/holyarrs", "-healthcheck"]
ENTRYPOINT ["/holyarrs"]
CMD ["-config", "/config"]

# The "ai" image (build with --target ai): builds on gpu (full Vulkan, VAAPI,
# and QSV transcoding) plus the Vulkan llama-server translator and whisper
# speech-to-text runners.
FROM gpu AS ai
ARG VERSION=dev
LABEL org.opencontainers.image.title="HolyArrs (with hardware transcoding, built-in translator and speech to text)" \
      org.opencontainers.image.source="https://github.com/HolyArrs/HolyArrs" \
      org.opencontainers.image.licenses="AGPL-3.0-only AND MIT AND GPL-3.0-or-later AND LGPL-2.1-or-later" \
      org.opencontainers.image.version="$VERSION"
RUN --mount=type=cache,id=dnf-fedora44,target=/var/cache/libdnf5,sharing=locked \
    dnf install -y --setopt=install_weak_deps=False --setopt=keepcache=True \
        libgomp openssl-libs zlib-ng-compat
COPY --from=llama /out/llama/ /opt/llama/
COPY --from=whisper /out/whisper/ /opt/whisper/
RUN mv /opt/llama/LICENSE /LICENSE.llama.cpp \
    && mv /opt/whisper/LICENSE /LICENSE.whisper.cpp

# The "ai-intel" image (build with --target ai-intel): builds on gpu (Intel
# media driver, VPL, VAAPI, VMAF models, full FFmpeg) plus OpenVINO GenAI
# speech to text and translation for Intel Arc graphics cards.
# Built on Fedora 44 for amd64 only (intel-compute-runtime is x86_64-only).
FROM gpu AS ai-intel
ARG TARGETARCH
ARG VERSION=dev
ARG OPENVINO_GENAI_PIP="openvino-genai openvino-tokenizers huggingface-hub numpy"
ARG SILERO_VAD_TAG=v6.2.3
ARG SILERO_VAD_SHA256=7ed98ddbad84ccac4cd0aeb3099049280713df825c610a8ed34543318f1b2c49
LABEL org.opencontainers.image.title="HolyArrs (Intel Arc OpenVINO speech to text and translation, and hardware transcoding)" \
      org.opencontainers.image.source="https://github.com/HolyArrs/HolyArrs" \
      org.opencontainers.image.licenses="AGPL-3.0-only AND MIT AND GPL-3.0-or-later AND LGPL-2.1-or-later AND Apache-2.0" \
      org.opencontainers.image.version="$VERSION"
RUN --mount=type=cache,id=dnf-fedora44,target=/var/cache/libdnf5,sharing=locked \
    --mount=type=cache,target=/root/.cache/pip \
    set -eu; \
    case "${TARGETARCH:-$(uname -m)}" in \
        amd64|x86_64) ;; \
        *) echo "the ai-intel image is built for amd64 only" >&2; exit 1 ;; \
    esac; \
    dnf install -y --setopt=install_weak_deps=False --setopt=keepcache=True \
        python3 python3-pip libgomp openssl-libs zlib-ng-compat \
        intel-compute-runtime intel-level-zero oneapi-level-zero intel-opencl OpenCL-ICD-Loader intel-ocloc; \
    python3 -m venv /opt/openvino-venv; \
    /opt/openvino-venv/bin/pip install $OPENVINO_GENAI_PIP; \
    test -e /usr/lib64/libOpenCL.so.1; \
    plugin="$(echo /opt/openvino-venv/lib64/python*/site-packages/openvino/libs/libopenvino_intel_gpu_plugin.so)"; \
    test -f "$plugin"; \
    ! LD_LIBRARY_PATH="$(dirname "$plugin")" ldd "$plugin" | grep -q "not found"
RUN mkdir -p /opt/whisper /opt/llama \
    && printf '#!/bin/sh\nexec /opt/openvino-venv/bin/python3 /opt/llama/llm_openvino_server.py "$@"\n' \
        > /opt/llama/llama-server \
    && curl -fsSL -o /opt/whisper/silero_vad.onnx \
        "https://raw.githubusercontent.com/snakers4/silero-vad/${SILERO_VAD_TAG}/src/silero_vad/data/silero_vad_16k_op15.onnx" \
    && echo "${SILERO_VAD_SHA256}  /opt/whisper/silero_vad.onnx" | sha256sum -c - \
    && printf '#!/bin/sh\nexec /opt/openvino-venv/bin/python3 /opt/whisper/whisper_openvino_server.py "$@"\n' \
        > /opt/whisper/whisper-server \
    && printf '#!/bin/sh\necho "ai-intel uses /opt/whisper/whisper-server (OpenVINO GenAI)" >&2\nexit 1\n' \
        > /opt/whisper/whisper-cli \
    && chmod 0644 /opt/whisper/silero_vad.onnx \
    && chmod 0755 /opt/llama/llama-server /opt/whisper/whisper-server /opt/whisper/whisper-cli
COPY --chmod=0644 deploy/intel/openvino_common.py /opt/whisper/openvino_common.py
COPY --chmod=0644 deploy/intel/openvino_common.py /opt/llama/openvino_common.py
COPY --chmod=0755 deploy/intel/whisper_openvino_server.py /opt/whisper/whisper_openvino_server.py
COPY --chmod=0755 deploy/intel/llm_openvino_server.py /opt/llama/llm_openvino_server.py
ENV LIBVA_DRIVER_NAME=iHD \
    NEOReadDebugKeys=1 \
    OverrideGpuAddressSpace=48 \
    HOLYARRS_OV_TRANSLATE=1

# Final stage: an empty image with only HolyArrs, CA certificates and the
# licences. No shell and no system tools: HolyArrs is one static Go program
# with its own time zone database, RAR/7z/zip reader and healthcheck.
FROM scratch
ARG VERSION=dev
LABEL org.opencontainers.image.title="HolyArrs" \
      org.opencontainers.image.source="https://github.com/HolyArrs/HolyArrs" \
      org.opencontainers.image.licenses="AGPL-3.0-only" \
      org.opencontainers.image.version="$VERSION"
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/holyarrs /holyarrs
COPY --from=build /out/dirs/ /
COPY LICENSE /LICENSE
# No GOMEMLIMIT here: the program aims at 384 MiB by itself, and on a small
# machine or a container limited to about 2.5 GiB or less it turns on its
# low-memory mode and aims lower (a quarter of the memory, 192-384 MiB; see
# docs/run/parts-safe-mode.md). SQLite's page caches and the driver's own heap
# sit outside that target (measured up to about 69 MiB with a 12,800-Item
# Library), so about 20 percent of a 500 MB limit stays headroom.
# Set GOMEMLIMIT to choose the target.
# No USER line: the image starts as root, gives /config to PUID:PGID (default
# 1000:1000) when its owner differs, then restarts itself as that user and
# never keeps running as root. Run with user: "<uid>:<gid>" to skip the switch.
# container=oci marks this as a container on every runtime: Docker and Podman
# leave marker files, but containerd (Kubernetes, k3s, nerdctl) does not, and
# outside a container a root start without PUID/PGID is refused.
ENV container=oci
EXPOSE 6262
VOLUME ["/config"]
HEALTHCHECK --interval=30s --timeout=10s --start-period=10s --retries=3 CMD ["/holyarrs", "-healthcheck"]
ENTRYPOINT ["/holyarrs"]
# No -addr here, so HOLYARRS_ADDR or HOLYARRS_PORT can move the port (default 6262).
CMD ["-config", "/config"]
