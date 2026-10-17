# syntax=docker/dockerfile:1.7

# These are multi-architecture OCI indexes, pinned by digest. Update them only
# after reviewing the upstream release, digest, and the resulting CI image.
ARG NODE_IMAGE=node:22.14.0-bookworm-slim@sha256:1c18d9ab3af4585870b92e4dbc5cac5a0dc77dd13df1a5905cea89fc720eb05b
ARG GO_IMAGE=golang:1.23.8-bookworm@sha256:0b4f8c5d414756a53ba0e4d235151f9b246552e512c3feaad5f9a3251376b279
ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

FROM ${NODE_IMAGE} AS frontend-build
WORKDIR /src/frontend
COPY frontend/package.json frontend/package-lock.json ./
RUN npm ci
COPY frontend/ ./
RUN npm run build

FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS server-build
ARG TARGETOS
ARG TARGETARCH
ENV CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH}
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
COPY --from=frontend-build /src/internal/web/dist ./internal/web/dist
RUN go build -trimpath -buildvcs=false -ldflags='-s -w' -o /out/portfolio-dashboard ./cmd/portfolio-dashboard

FROM ${RUNTIME_IMAGE} AS runtime
WORKDIR /app
COPY --from=server-build /out/portfolio-dashboard /app/portfolio-dashboard
VOLUME ["/data"]
EXPOSE 8080
USER 65532:65532
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 CMD ["/app/portfolio-dashboard", "healthcheck"]
ENTRYPOINT ["/app/portfolio-dashboard"]
