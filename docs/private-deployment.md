# Private deployment package

This repository now contains the source package for an operator-managed private deployment. It does not deploy anything, terminate TLS, configure DNS, or contain portfolio data or credentials.

## Build and publication

`.github/workflows/container.yml` builds `linux/amd64` and `linux/arm64` for pull requests without publishing. A push to merged `main` publishes only the full commit SHA tag to GHCR, with `packages:write` permission limited to that publication job. The publish job enables BuildKit max provenance and SBOM attestations and writes the resulting multi-platform digest to the workflow summary and job output.

The Dockerfile uses these immutable upstream references:

- `node:22.14.0-bookworm-slim@sha256:1c18d9ab3af4585870b92e4dbc5cac5a0dc77dd13df1a5905cea89fc720eb05b`
- `golang:1.23.8-bookworm@sha256:0b4f8c5d414756a53ba0e4d235151f9b246552e512c3feaad5f9a3251376b279`
- `gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab`

To update a base, review the upstream version release and multi-platform digest, replace both the version and digest in `Dockerfile`, and run the normal PR checks. Do not replace a digest with a mutable tag. Action references in the workflow are similarly pinned to full commit SHAs; update their version comment and SHA together after reviewing the maintained action release.

No local multi-platform image build is claimed by this repository. The pull-request and merged-main workflow jobs are the verification path.

## Operator setup

1. The tracked root `compose.yaml` and `.env.example` are the primary deployment files. Copy `.env.example` to `.env`, set mode `0600`, and replace the required `REPLACE_WITH_*` values. Leave `PORTFOLIO_DASHBOARD_IMAGE` blank to use the default, or set it to the exact published full-commit tag plus its `@sha256:` digest from the merged-main workflow summary for immutable pinning. Leave optional machine/provider entries blank unless enabling those features. Set `PORTFOLIO_DATA_DIR` to an operator-owned directory; the example supplies safe defaults for container UID/GID (`65532`), host port (`8080`), and reporting currency (`MYR`). Compose automatically reads the root `.env` file:

   ```sh
   cp .env.example .env
   chmod 600 .env
   # Edit .env using the operator's secret manager/editor.
   docker compose -f compose.yaml config
   ```

   The exact `https://host[:port]` public origin, login username, bcrypt password hash, and independent random session secret are required. Leave machine credentials and provider keys blank unless their feature is explicitly enabled. Provider keys and the optional machine bearer secret are injected from `.env` only; they are not in the image, SQLite database, URLs, or logs. Both machine grant flags must remain `false` while machine credentials are blank.
3. Choose the host data directory and numeric container identity. The default container identity is UID/GID `65532`; if the deployment user has different IDs, set `PORTFOLIO_CONTAINER_UID` and `PORTFOLIO_CONTAINER_GID` in `.env` before creating the directory. Make the bind mount owned by those same IDs so SQLite is created and migrated by the application user, not root:

   ```sh
   install -d -m 0700 /path/to/portfolio-dashboard-data
   sudo chown 65532:65532 /path/to/portfolio-dashboard-data
   # Set PORTFOLIO_DATA_DIR=/path/to/portfolio-dashboard-data in .env.
   ```

4. Review the rendered Compose configuration, then start it. The default host binding is loopback-only (`127.0.0.1:8080`); TLS and any public reverse proxy are intentionally outside this package. The application listens on port 8080 and stores SQLite at `/data/portfolio-dashboard.sqlite`.

   ```sh
   docker compose -f compose.yaml up -d
   docker compose -f compose.yaml ps
   ```

The runtime image runs as non-root with a read-only root filesystem, a bounded non-executable `/tmp`, no Linux capabilities, and `no-new-privileges`. Only `/data` is writable. The image healthcheck invokes the application binary's restricted `healthcheck` command, which can call only an HTTP(S) `/healthz` URL on literal loopback with an explicit port, never follows redirects or environment proxies, has a bounded timeout, reads at most 4 KiB, and accepts only HTTP 200.

## After publication: verify the immutable result

On the trusted operator host, compare the workflow summary digest with the registry resolution before replacing the example image line. For example, after substituting the actual owner, full SHA tag, and digest:

```sh
docker buildx imagetools inspect ghcr.io/OWNER/portfolio-dashboard:FULL_COMMIT_SHA@sha256:PUBLISHED_DIGEST
```

The resolved index digest and both `linux/amd64` and `linux/arm64` manifests must match the workflow output. This is a post-publication operator step, not a claim that it has been performed locally.
