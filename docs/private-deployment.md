# Private deployment package

This repository now contains the source package for an operator-managed private deployment. It does not deploy anything, terminate TLS, configure DNS, or contain portfolio data or credentials.

## Build and publication

`.github/workflows/container.yml` builds `linux/amd64` and `linux/arm64` for pull requests without publishing. A push to merged `main` publishes both the mutable `main` tag and the full commit SHA tag to GHCR for the same multi-platform OCI index, with `packages:write` permission limited to that publication job. The publish job enables BuildKit max provenance and SBOM attestations and writes the resulting multi-platform digest to the workflow summary and job output.

The Dockerfile uses these immutable upstream references:

- `node:22.14.0-bookworm-slim@sha256:1c18d9ab3af4585870b92e4dbc5cac5a0dc77dd13df1a5905cea89fc720eb05b`
- `golang:1.23.8-bookworm@sha256:0b4f8c5d414756a53ba0e4d235151f9b246552e512c3feaad5f9a3251376b279`
- `gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab`

To update a base, review the upstream version release and multi-platform digest, replace both the version and digest in `Dockerfile`, and run the normal PR checks. Do not replace a digest with a mutable tag. Action references in the workflow are similarly pinned to full commit SHAs; update their version comment and SHA together after reviewing the maintained action release.

No local multi-platform image build is claimed by this repository. The pull-request and merged-main workflow jobs are the verification path.

## Operator setup

1. The tracked root `compose.yaml` and `.env.example` are the primary deployment files. Copy `.env.example` to `.env`, set mode `0600`, and replace the required `REPLACE_WITH_*` values. Leave optional machine/provider entries blank unless enabling those features. Compose creates and manages the `portfolio-data` named volume automatically; no host data directory or ownership setup is required. The service runs as fixed non-root UID/GID `65532:65532`, and the database remains `/data/portfolio-dashboard.sqlite` inside the service. Compose uses the literal repository-owned `ghcr.io/boubonming/portfolio-dashboard:main` image and automatically reads the root `.env` file:

   ```sh
   cp .env.example .env
   chmod 600 .env
   # Edit .env using the operator's secret manager/editor.
   docker compose -f compose.yaml config
   ```

   The exact `https://host[:port]` public origin, login username, bcrypt password hash, and independent random session secret are required. Generate the bcrypt hash interactively, without putting the plaintext password on the command line:

   ```sh
   htpasswd -nBC 12 portfolio-admin
   ```

   Enter and confirm the password at the prompts. The output includes `portfolio-admin:` followed by the bcrypt value; copy only the bcrypt portion after the first colon, not `portfolio-admin:`. Store the complete bcrypt value in single quotes in `.env`, for example `PORTFOLIO_AUTH_PASSWORD_HASH='REPLACE_WITH_COMPLETE_BCRYPT_VALUE'`. Never paste the hash or plaintext password into chat, and never commit either one. Leave machine credentials and provider keys blank unless their feature is explicitly enabled. Provider keys and the optional machine bearer secret are injected from `.env` only; they are not in the image, SQLite database, URLs, or logs. Both machine grant flags must remain `false` while machine credentials are blank.

   The optional machine configuration is a least-privilege external Hermes bearer identity, not an embedded runtime. It consists of the three identity/secret values (`PORTFOLIO_MACHINE_PRINCIPAL_ID`, `PORTFOLIO_MACHINE_PORTFOLIO_ID`, and `PORTFOLIO_MACHINE_BEARER_SECRET`) plus both grants (`PORTFOLIO_MACHINE_PORTFOLIO_READ` and `PORTFOLIO_MACHINE_REVIEWS_WRITE`). It is disabled fail closed when the three values are blank and both grants are `false`; normal Hermes review generation requires all three values and both grants `true`. The identity is bound to one portfolio, can read snapshot/context data and submit immutable advisory reviews, but cannot edit holdings or trade. Market-provider credentials are optional and independent of these machine settings.
3. Review the rendered Compose configuration, then start it. The default host binding is loopback-only (`127.0.0.1:8080`); TLS and any public reverse proxy are intentionally outside this package. The application listens on port 8080 and stores SQLite at `/data/portfolio-dashboard.sqlite`. Ordinary `docker compose up -d` uses the locally cached `:main` image when present. To deliberately update the image, pull it first and then start the service:

   ```sh
   docker compose pull
   docker compose up -d
   docker compose ps
   ```

The Compose volume key is `portfolio-data`, but Compose may prefix the actual Docker volume name with the project name. Inspect the actual mounted name from the running service before maintenance:

```sh
docker inspect "$(docker compose ps -q portfolio-dashboard)" \
  --format '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Name}}{{end}}{{end}}'
```

For an offline backup, stop the service first and stream the database through a temporary helper container; the application remains non-root and is not started:

```sh
mkdir -p backups
VOLUME_NAME="$(docker inspect "$(docker compose ps -q portfolio-dashboard)" \
  --format '{{range .Mounts}}{{if eq .Destination "/data"}}{{.Name}}{{end}}{{end}}')"
docker compose stop portfolio-dashboard
docker run --rm --user 65532:65532 \
  -v "${VOLUME_NAME}:/data:ro" \
  busybox:1.36.1 tar czf - -C /data portfolio-dashboard.sqlite \
  > backups/portfolio-dashboard.sqlite.tgz
docker compose start portfolio-dashboard
```

Restore offline by stopping the service, extracting the backup into the same volume with the fixed runtime identity, and starting the service again:

```sh
docker compose stop portfolio-dashboard
cat backups/portfolio-dashboard.sqlite.tgz | docker run -i --rm --user 65532:65532 \
  -v "${VOLUME_NAME}:/data" \
  busybox:1.36.1 sh -c 'rm -f /data/portfolio-dashboard.sqlite && tar xzf - -C /data'
docker compose start portfolio-dashboard
```

The runtime image runs as non-root with a read-only root filesystem, a bounded non-executable `/tmp`, no Linux capabilities, and `no-new-privileges`. Only `/data` is writable. The image healthcheck invokes the application binary's restricted `healthcheck` command, which can call only an HTTP(S) `/healthz` URL on literal loopback with an explicit port, never follows redirects or environment proxies, has a bounded timeout, reads at most 4 KiB, and accepts only HTTP 200.

## After publication: verify the published result

On the trusted operator host, compare the workflow summary digest with the registry resolution when recording build provenance or preparing a rollback reference. The merged-main workflow updates the mutable `main` tag and also publishes the full-SHA tag to the same digest. The full-SHA tag and published digest are not required deployment settings. For example, after substituting the actual owner, full SHA tag, and digest:

```sh
docker buildx imagetools inspect ghcr.io/OWNER/portfolio-dashboard:FULL_COMMIT_SHA@sha256:PUBLISHED_DIGEST
```

The resolved index digest and both `linux/amd64` and `linux/arm64` manifests must match the workflow output. This is a post-publication operator step, not a claim that it has been performed locally.
