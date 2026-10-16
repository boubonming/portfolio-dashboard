package deployment_test

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

func repoFile(t *testing.T, name string) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	data, err := os.ReadFile(filepath.Join(root, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

func TestDockerfileProductionInvariants(t *testing.T) {
	dockerfile := repoFile(t, "Dockerfile")
	for _, required := range []string{
		"node:22.14.0-bookworm-slim@sha256:",
		"golang:1.23.8-bookworm@sha256:",
		"gcr.io/distroless/static-debian12:nonroot@sha256:",
		"npm ci",
		"GOOS=${TARGETOS} GOARCH=${TARGETARCH}",
		"CGO_ENABLED=0",
		"USER 65532:65532",
		"EXPOSE 8080",
		"HEALTHCHECK",
		"healthcheck",
	} {
		if !strings.Contains(dockerfile, required) {
			t.Errorf("Dockerfile missing %q", required)
		}
	}
	if strings.Contains(strings.ToLower(dockerfile), "curl") || strings.Contains(strings.ToLower(dockerfile), "wget") {
		t.Error("runtime package must not add curl or wget for healthchecks")
	}
	for _, match := range regexp.MustCompile(`@sha256:[0-9a-f]{64}`).FindAllString(dockerfile, -1) {
		if len(match) != len("@sha256:")+64 {
			t.Errorf("invalid immutable image reference %q", match)
		}
	}
}

func TestComposeOperatorInvariants(t *testing.T) {
	compose := repoFile(t, "deploy/compose.private.example.yml")
	for _, required := range []string{
		"@sha256:REPLACE_WITH_PUBLISHED_IMAGE_DIGEST",
		"PORTFOLIO_DATA_PATH: \"/data/portfolio-dashboard.sqlite\"",
		"127.0.0.1:",
		"read_only: true",
		"no-new-privileges:true",
		"- ALL",
		"./private.env",
		"PORTFOLIO_PUBLIC_ORIGIN",
		"PORTFOLIO_AUTH_PASSWORD_HASH",
		"PORTFOLIO_SESSION_SECRET",
		"/app/portfolio-dashboard",
		"/healthz",
	} {
		if !strings.Contains(compose, required) {
			t.Errorf("Compose source missing %q", required)
		}
	}
	if strings.Contains(strings.ToLower(compose), ":latest") {
		t.Error("Compose source must not use latest")
	}
}

func TestWorkflowPublicationInvariants(t *testing.T) {
	workflow := repoFile(t, ".github/workflows/container.yml")
	for _, required := range []string{
		"pull_request:",
		"push:",
		"type=raw,value=${{ github.sha }}",
		"platforms: linux/amd64,linux/arm64",
		"push: false",
		"push: true",
		"packages: write",
		"provenance: mode=max",
		"sbom: true",
		"steps.build.outputs.digest",
		"GITHUB_STEP_SUMMARY",
	} {
		if !strings.Contains(workflow, required) {
			t.Errorf("workflow missing %q", required)
		}
	}
	for _, action := range []string{"actions/checkout@", "docker/setup-qemu-action@", "docker/setup-buildx-action@", "docker/login-action@", "docker/metadata-action@", "docker/build-push-action@"} {
		for _, line := range strings.Split(workflow, "\n") {
			if strings.Contains(line, "uses: "+action) && !regexp.MustCompile(`@[0-9a-f]{40}(?:\s|#|$)`).MatchString(line) {
				t.Errorf("action is not pinned to a full commit SHA: %s", line)
			}
		}
	}
}
