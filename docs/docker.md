# Docker

```bash
make docker-build   # docker build -t imagelet:dev .
make docker-run     # docker run --rm -p 8080:8080 imagelet:dev
```

The image is multi-stage: `golang:1.25-bookworm` builder cross-compiles a static
binary, runtime is `gcr.io/distroless/static-debian12:nonroot`. Highlights:

- Distroless static base — no shell, no package manager, no glibc.
- Runs as `nonroot` (UID 65532).
- ~20 MB total. `tzdata` is embedded in the binary, so `time.LoadLocation` works
  without `/usr/share/zoneinfo` on the host.
- Multi-arch source — one Dockerfile, both `linux/amd64` and `linux/arm64`.

For hardened deployments, run with read-only rootfs — distroless static needs
no writable filesystem at runtime:

```bash
docker run --rm --read-only --tmpfs /tmp -p 8080:8080 imagelet:dev
```

Override the image tag:

```bash
DOCKER_IMAGE=myorg/imagelet:1.2.3 make docker-build
```

Build for both architectures (buildx; needs a registry to actually push):

```bash
docker buildx build --platform linux/amd64,linux/arm64 -t imagelet:dev .
```

The image has no `HEALTHCHECK` directive — distroless lacks the shell and HTTP
client a probe would need, and orchestrators (Kubernetes, Compose, Fly machines,
...) supply their own. `GET /healthz` returns `200 No Content` and is the
intended liveness probe — it never renders, never reaches an upstream, never
allocates. (`GET /` is now a rendered landing page and unsuitable for high-rate
probes.)

## Image releases

Pre-built multi-arch images are published to `ghcr.io/cmj0121/imagelet` by
`.github/workflows/release.yml`:

```bash
docker pull ghcr.io/cmj0121/imagelet:latest
```

Available tags: `latest` (most recent semver release), `vX.Y.Z` (exact release),
`X.Y` (latest patch in that minor), `main-<sha>` (every push to `main`). Pin
something other than `latest` for production.

ghcr packages default to **private**. After the first publish, flip the package
visibility to public via the GitHub UI if you want unauthenticated `docker pull`
to work — this is a one-time per-package action, not workflow-controllable.
