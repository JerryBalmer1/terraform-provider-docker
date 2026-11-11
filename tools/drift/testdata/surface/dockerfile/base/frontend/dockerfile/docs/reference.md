# Dockerfile reference

## Parser directives

### syntax

### escape

## FROM

```dockerfile
FROM [--platform=<platform>] <image> [AS <name>]
```

## RUN

### RUN --mount

### RUN --mount=type=cache

## MAINTAINER (deprecated)

## HEALTHCHECK

- `--interval=DURATION` (default: `30s`)

## ONBUILD

```dockerfile
ONBUILD RUN --mount=type=bind,target=/x make
```

## ARG

### Predefined ARGs

- `HTTP_PROXY`

### BuildKit built-in build args

| Arg                    | Type | Description |
|------------------------|------|-------------|
| `BUILDKIT_INLINE_CACHE` | Bool | Inline cache |
