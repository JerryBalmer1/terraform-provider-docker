# syntax=docker/dockerfile:1
FROM debian AS base
RUN <<EOT bash
  set -ex
  apt-get update
  apt-get install -y vim
EOT
RUN <<-"EOF" cat > /run.sh
	#!/bin/sh
	echo "$HOME"
	EOF
COPY <<robots.txt <<humans.txt /usr/share/nginx/html/
User-agent: *
Disallow: /
robots.txt
Humans: we
humans.txt
RUN python3 <<EOF
print("hello")
EOF
HEALTHCHECK NONE
MAINTAINER Jane Doe <jane@example.com>
CMD -- --ignored-param1
