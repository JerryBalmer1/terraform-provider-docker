# syntax=docker/dockerfile:1
ARG BASE=busybox:1.36

FROM ${BASE} AS build
WORKDIR /src
RUN echo "built at $(date -u +%Y-%m-%dT%H:%M:%SZ)" > /src/build-info.txt

FROM ${BASE} AS runtime
LABEL org.opencontainers.image.title="manual-dockerfile"
COPY --from=build /src/build-info.txt /srv/build-info.txt
COPY index.html /srv/index.html
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s CMD wget -q -O- http://127.0.0.1:8080/ || exit 1
CMD ["httpd", "-f", "-p", "8080", "-h", "/srv"]
