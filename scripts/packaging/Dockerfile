# The runtime wraps the verified Linux archive; it never rebuilds application code.
FROM ubuntu:24.04@sha256:b3cc40b72b93588182b5410f723c7aaf142363311c2aa993d8a453ddcbb3ae15

RUN apt-get update \
    && apt-get install --yes --no-install-recommends \
       bash bubblewrap ca-certificates curl git python3 python3-venv xz-utils \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --uid 10001 --create-home --home-dir /var/lib/synon-biomed synon

ARG RELEASE_VERSION
ARG SOURCE_REVISION
LABEL org.opencontainers.image.title="Synon Biomed" \
      org.opencontainers.image.description="Biomedical workspace with on-demand scientific environments" \
      org.opencontainers.image.licenses="AGPL-3.0-only" \
      org.opencontainers.image.source="https://github.com/Victor-Xu-1/synon-biomed" \
      org.opencontainers.image.version="${RELEASE_VERSION}" \
      org.opencontainers.image.revision="${SOURCE_REVISION}"

COPY --chown=root:root synon-biomed/ /opt/synon-biomed/
ENV HOME=/var/lib/synon-biomed \
    SYNON_HOME=/var/lib/synon-biomed \
    SYNON_ADDRESS=0.0.0.0:8765
WORKDIR /opt/synon-biomed
USER 10001:10001
EXPOSE 8765
HEALTHCHECK --interval=30s --timeout=5s --start-period=60s \
    CMD curl --fail --silent http://127.0.0.1:8765/api/health > /dev/null || exit 1
ENTRYPOINT ["/opt/synon-biomed/synon-go"]
