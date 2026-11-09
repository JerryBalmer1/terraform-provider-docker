# syntax=docker/dockerfile:1.7
# check=skip=JSONArgsRecommended;error=true

ARG GO_VERSION=1.22
ARG BASE="alpine:3.19"

# build stage
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION} AS build
WORKDIR /src
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=bind,source=go.mod,target=go.mod \
    go mod download
COPY --link --chmod=0755 --exclude=*.md . .
RUN --network=none --security=sandbox go build -o /out/app ./cmd/app
RUN --mount=type=secret,id=aws,env=AWS_KEY,required=true ./upload.sh

FROM ${BASE}
LABEL org.opencontainers.image.authors="Ops Team <ops@example.com>" version=1.0
ENV APP_HOME=/app PATH="/app/bin:$PATH"
ENV LEGACY value with spaces
COPY --from=build --chown=app:app /out/app /app/bin/app
ADD --checksum=sha256:24454f830cdb571e2c4ad15481119c43b3cafd48dd869a9b2945d1036d1dc68d https://example.com/file.tar.gz /tmp/
ADD --keep-git-dir=true https://github.com/moby/buildkit.git#v0.10.1 /buildkit
EXPOSE 8080 9090/udp
VOLUME ["/data", "/var/log app"]
USER app:app
HEALTHCHECK --interval=5m --timeout=3s --retries=3 CMD curl -f http://localhost/ || exit 1
STOPSIGNAL SIGTERM
SHELL ["/bin/sh", "-c"]
ONBUILD COPY --from=build /out /onbuild
ONBUILD RUN echo triggered
ENTRYPOINT ["/app/bin/app"]
CMD ["--serve"]
