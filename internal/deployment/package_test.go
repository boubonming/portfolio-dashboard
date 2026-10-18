package deployment_test

import (
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func repoFile(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot(t), name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
}

func yamlMap(t *testing.T, value any, name string) map[string]any {
	t.Helper()
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("%s has type %T, want mapping", name, value)
	}
	return result
}

func yamlList(t *testing.T, value any, name string) []any {
	t.Helper()
	result, ok := value.([]any)
	if !ok {
		t.Fatalf("%s has type %T, want sequence", name, value)
	}
	return result
}

func yamlString(t *testing.T, value any, name string) string {
	t.Helper()
	result, ok := value.(string)
	if !ok {
		t.Fatalf("%s has type %T, want string", name, value)
	}
	return result
}

func yamlBool(t *testing.T, value any, name string) bool {
	t.Helper()
	result, ok := value.(bool)
	if !ok {
		t.Fatalf("%s has type %T, want bool", name, value)
	}
	return result
}

func parseYAML(t *testing.T, name string) map[string]any {
	t.Helper()
	var document map[string]any
	if err := yaml.Unmarshal([]byte(repoFile(t, name)), &document); err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return document
}

func stringList(t *testing.T, value any, name string) []string {
	t.Helper()
	items := yamlList(t, value, name)
	result := make([]string, len(items))
	for i, item := range items {
		result[i] = yamlString(t, item, fmt.Sprintf("%s[%d]", name, i))
	}
	return result
}

func requireStrings(t *testing.T, got []string, want ...string) {
	t.Helper()
	seen := make(map[string]bool, len(got))
	for _, value := range got {
		seen[value] = true
	}
	for _, value := range want {
		if !seen[value] {
			t.Errorf("missing %q from %v", value, got)
		}
	}
}

func workflowStep(t *testing.T, steps []any, name string) map[string]any {
	t.Helper()
	for i, item := range steps {
		step := yamlMap(t, item, fmt.Sprintf("step[%d]", i))
		if step["name"] == name {
			return step
		}
	}
	t.Fatalf("workflow step %q not found", name)
	return nil
}

func TestDockerfileProductionInvariants(t *testing.T) {
	dockerfile := repoFile(t, "Dockerfile")
	lines := strings.Split(dockerfile, "\n")
	from := make([]string, 0, 3)
	instructions := make(map[string][]string)
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		parts := strings.SplitN(trimmed, " ", 2)
		instructions[strings.ToUpper(parts[0])] = append(instructions[strings.ToUpper(parts[0])], trimmed)
		if strings.HasPrefix(trimmed, "FROM ") {
			from = append(from, trimmed)
		}
	}
	if len(from) != 3 {
		t.Fatalf("FROM instructions = %d, want three production stages: %v", len(from), from)
	}
	if from[0] != "FROM ${NODE_IMAGE} AS frontend-build" || from[1] != "FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS server-build" || from[2] != "FROM ${RUNTIME_IMAGE} AS runtime" {
		t.Fatalf("stages = %v", from)
	}
	expectedArgs := map[string]string{
		"NODE_IMAGE":    "node:22.14.0-bookworm-slim@sha256:1c18d9ab3af4585870b92e4dbc5cac5a0dc77dd13df1a5905cea89fc720eb05b",
		"GO_IMAGE":      "golang:1.23.8-bookworm@sha256:0b4f8c5d414756a53ba0e4d235151f9b246552e512c3feaad5f9a3251376b279",
		"RUNTIME_IMAGE": "gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab",
	}
	for name, value := range expectedArgs {
		want := "ARG " + name + "=" + value
		found := false
		for _, line := range instructions["ARG"] {
			found = found || line == want
		}
		if !found {
			t.Errorf("missing exact immutable %s", want)
		}
	}
	for _, want := range []string{
		"RUN npm ci",
		"RUN npm run build",
		"ENV CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH}",
		"RUN go mod download",
		"RUN go build -trimpath -buildvcs=false -ldflags='-s -w' -o /out/portfolio-dashboard ./cmd/portfolio-dashboard",
		"USER 65532:65532",
		"EXPOSE 8080",
		"HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 CMD [\"/app/portfolio-dashboard\", \"healthcheck\"]",
		"ENTRYPOINT [\"/app/portfolio-dashboard\"]",
	} {
		found := false
		for _, line := range lines {
			if strings.TrimSpace(line) == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing exact Dockerfile instruction %q", want)
		}
	}
	if got := regexp.MustCompile(`@sha256:[0-9a-f]{64}`).FindAllString(dockerfile, -1); len(got) != 3 {
		t.Fatalf("immutable digest count = %d (%v), want three", len(got), got)
	}
	if strings.Contains(strings.ToLower(dockerfile), "curl") || strings.Contains(strings.ToLower(dockerfile), "wget") {
		t.Error("runtime package must not add curl or wget for healthchecks")
	}
}

func TestFrontendArtifactPathMatchesDockerfile(t *testing.T) {
	viteConfig := repoFile(t, "frontend/vite.config.ts")
	outDir := regexp.MustCompile(`(?m)\boutDir:\s*['"]([^'"]+)['"]`).FindStringSubmatch(viteConfig)
	if len(outDir) != 2 {
		t.Fatalf("frontend Vite config does not declare one build.outDir")
	}

	dockerfile := repoFile(t, "Dockerfile")
	frontendStage := regexp.MustCompile(`(?ms)^FROM [^\n]+ AS frontend-build\n(.*?)^FROM `).FindStringSubmatch(dockerfile)
	if len(frontendStage) != 2 {
		t.Fatalf("Dockerfile does not declare a frontend-build stage")
	}
	workdir := regexp.MustCompile(`(?m)^WORKDIR\s+(\S+)\s*$`).FindStringSubmatch(frontendStage[1])
	if len(workdir) != 2 {
		t.Fatalf("frontend-build stage does not declare a WORKDIR")
	}

	copy := regexp.MustCompile(`(?m)^COPY --from=frontend-build\s+(\S+)\s+(\S+)\s*$`).FindStringSubmatch(dockerfile)
	if len(copy) != 3 {
		t.Fatalf("Dockerfile does not copy one frontend-build artifact")
	}
	wantSource := path.Join(workdir[1], outDir[1])
	if copy[1] != wantSource {
		t.Fatalf("frontend artifact source = %q, want resolved Vite output %q", copy[1], wantSource)
	}
	if copy[2] != "./internal/web/dist" {
		t.Fatalf("frontend artifact destination = %q, want Go embed path ./internal/web/dist", copy[2])
	}
}

func TestComposeOperatorInvariantsAreStructured(t *testing.T) {
	document := parseYAML(t, "compose.yaml")
	service := yamlMap(t, yamlMap(t, document["services"], "services")["portfolio-dashboard"], "portfolio-dashboard")
	image := yamlString(t, service["image"], "service.image")
	if image != "${PORTFOLIO_DASHBOARD_IMAGE:-ghcr.io/boubonming/portfolio-dashboard:main}" {
		t.Fatalf("image = %q, want the runnable repository-owned default with an override", image)
	}
	if strings.Contains(image, "REPLACE_WITH_") {
		t.Fatalf("image %q contains a placeholder", image)
	}
	if yamlString(t, service["pull_policy"], "service.pull_policy") != "always" {
		t.Error("service.pull_policy is not always")
	}
	if yamlString(t, service["user"], "service.user") != "${PORTFOLIO_CONTAINER_UID:-65532}:${PORTFOLIO_CONTAINER_GID:-65532}" {
		t.Error("service user does not use the documented numeric UID/GID interpolation")
	}
	if !yamlBool(t, service["read_only"], "service.read_only") {
		t.Error("service is not read-only")
	}
	security := stringList(t, service["security_opt"], "service.security_opt")
	requireStrings(t, security, "no-new-privileges:true")
	requireStrings(t, stringList(t, service["cap_drop"], "service.cap_drop"), "ALL")
	requireStrings(t, stringList(t, service["tmpfs"], "service.tmpfs"), "/tmp:rw,noexec,nosuid,nodev,size=16m")
	requireStrings(t, stringList(t, service["ports"], "service.ports"), "127.0.0.1:${PORTFOLIO_HOST_PORT:-8080}:8080")
	requireStrings(t, stringList(t, service["volumes"], "service.volumes"), "${PORTFOLIO_DATA_DIR:?set PORTFOLIO_DATA_DIR to an operator-owned directory}:/data")
	requireStrings(t, stringList(t, service["env_file"], "service.env_file"), "./private.env")
	environment := yamlMap(t, service["environment"], "service.environment")
	expectedEnvironment := map[string]string{
		"PORTFOLIO_BIND_ADDRESS":           "0.0.0.0:8080",
		"PORTFOLIO_DATA_PATH":              "/data/portfolio-dashboard.sqlite",
		"PORTFOLIO_ENV":                    "production",
		"PORTFOLIO_COOKIE_SECURE":          "true",
		"PORTFOLIO_REPORTING_CURRENCY":     "${PORTFOLIO_REPORTING_CURRENCY:-MYR}",
		"PORTFOLIO_MACHINE_PORTFOLIO_READ": "${PORTFOLIO_MACHINE_PORTFOLIO_READ:-false}",
		"PORTFOLIO_MACHINE_REVIEWS_WRITE":  "${PORTFOLIO_MACHINE_REVIEWS_WRITE:-false}",
	}
	for name, want := range expectedEnvironment {
		if got := yamlString(t, environment[name], "environment."+name); got != want {
			t.Errorf("environment.%s = %q, want %q", name, got, want)
		}
	}
	for _, name := range []string{"PORTFOLIO_PUBLIC_ORIGIN", "PORTFOLIO_AUTH_USERNAME", "PORTFOLIO_AUTH_PASSWORD_HASH", "PORTFOLIO_SESSION_SECRET"} {
		value := yamlString(t, environment[name], "environment."+name)
		if !strings.Contains(value, "${"+name+":?") {
			t.Errorf("environment.%s is not required interpolation: %q", name, value)
		}
	}
	health := yamlMap(t, service["healthcheck"], "service.healthcheck")
	if got := stringList(t, health["test"], "healthcheck.test"); strings.Join(got, "\x00") != strings.Join([]string{"CMD", "/app/portfolio-dashboard", "healthcheck", "-url", "http://127.0.0.1:8080/healthz"}, "\x00") {
		t.Fatalf("healthcheck.test = %v", got)
	}
}

func TestTrackedComposeImagesHaveRunnableDefaults(t *testing.T) {
	for _, name := range []string{"compose.yaml", "deploy/compose.private.example.yml"} {
		document := parseYAML(t, name)
		service := yamlMap(t, yamlMap(t, document["services"], name+".services")["portfolio-dashboard"], name+".portfolio-dashboard")
		image := yamlString(t, service["image"], name+".image")
		if strings.Contains(image, "REPLACE_WITH_") {
			t.Errorf("%s contains an image placeholder: %q", name, image)
		}
		if image != "${PORTFOLIO_DASHBOARD_IMAGE:-ghcr.io/boubonming/portfolio-dashboard:main}" {
			t.Errorf("%s image = %q, want the repository-owned default", name, image)
		}
	}
}

func TestRootComposeMatchesDeployTemplate(t *testing.T) {
	composeNames := []string{"compose.yaml", "deploy/compose.private.example.yml"}
	documents := make([]map[string]any, len(composeNames))
	for i, name := range composeNames {
		document := parseYAML(t, name)
		documents[i] = document
		services := yamlMap(t, document["services"], name+".services")
		service := yamlMap(t, services["portfolio-dashboard"], name+".portfolio-dashboard")
		envFiles := stringList(t, service["env_file"], name+".env_file")
		if !reflect.DeepEqual(envFiles, []string{"./private.env"}) {
			t.Fatalf("%s env_file = %v, want exactly ./private.env", name, envFiles)
		}
		resolved := path.Clean(path.Join(path.Dir(name), envFiles[0]))
		wantResolved := path.Join(path.Dir(name), "private.env")
		if resolved != wantResolved {
			t.Fatalf("%s env_file resolves to %q, want %q", name, resolved, wantResolved)
		}
		// Compare the files as the same Compose document after mapping each
		// location-relative private.env to one logical file name.
		service["env_file"] = []any{"private.env"}
	}
	if !reflect.DeepEqual(documents[0], documents[1]) {
		t.Fatalf("root compose.yaml diverges from deploy/compose.private.example.yml:\nroot: %#v\ndeploy: %#v", documents[0], documents[1])
	}
}

func TestWorkflowTriggersAndPublicationAreStructured(t *testing.T) {
	document := parseYAML(t, ".github/workflows/container.yml")
	events := yamlMap(t, document["on"], "on")
	for _, eventName := range []string{"pull_request", "push"} {
		event := yamlMap(t, events[eventName], "on."+eventName)
		if got := stringList(t, event["paths"], "on."+eventName+".paths"); len(got) == 0 {
			t.Fatalf("on.%s.paths is empty", eventName)
		} else {
			requireStrings(t, got, "deploy/**", "compose.yaml", "docs/private-deployment.md")
		}
	}
	jobs := yamlMap(t, document["jobs"], "jobs")
	pr := yamlMap(t, jobs["build-pr"], "jobs.build-pr")
	if yamlString(t, pr["if"], "jobs.build-pr.if") != "github.event_name == 'pull_request'" {
		t.Error("pull request job condition changed")
	}
	prPermissions := yamlMap(t, pr["permissions"], "jobs.build-pr.permissions")
	if yamlString(t, prPermissions["contents"], "jobs.build-pr.permissions.contents") != "read" || len(prPermissions) != 1 {
		t.Errorf("pull request permissions = %#v", prPermissions)
	}
	prBuild := workflowStep(t, yamlList(t, pr["steps"], "jobs.build-pr.steps"), "Build both production platforms (no publish)")
	prWith := yamlMap(t, prBuild["with"], "pull request build with")
	if yamlString(t, prWith["platforms"], "pull request platforms") != "linux/amd64,linux/arm64" || yamlBool(t, prWith["push"], "pull request push") || yamlBool(t, prWith["provenance"], "pull request provenance") || yamlBool(t, prWith["sbom"], "pull request sbom") {
		t.Fatal("pull request build does not explicitly disable publication/attestations")
	}

	publish := yamlMap(t, jobs["publish-main"], "jobs.publish-main")
	if yamlString(t, publish["if"], "jobs.publish-main.if") != "github.event_name == 'push' && github.ref == 'refs/heads/main'" {
		t.Error("main publication condition changed")
	}
	permissions := yamlMap(t, publish["permissions"], "jobs.publish-main.permissions")
	if yamlString(t, permissions["contents"], "publish contents permission") != "read" || yamlString(t, permissions["packages"], "publish package permission") != "write" || len(permissions) != 2 {
		t.Errorf("publication permissions = %#v", permissions)
	}
	steps := yamlList(t, publish["steps"], "jobs.publish-main.steps")
	build := workflowStep(t, steps, "Build and publish both production platforms")
	with := yamlMap(t, build["with"], "publication build with")
	if yamlString(t, with["platforms"], "publication platforms") != "linux/amd64,linux/arm64" || !yamlBool(t, with["push"], "publication push") || yamlString(t, with["provenance"], "publication provenance") != "mode=max" || !yamlBool(t, with["sbom"], "publication sbom") {
		t.Fatal("publication build invariants changed")
	}
	metadata := workflowStep(t, steps, "Generate immutable image metadata")
	metadataWith := yamlMap(t, metadata["with"], "metadata with")
	if !strings.Contains(yamlString(t, metadataWith["tags"], "metadata tags"), "type=raw,value=${{ github.sha }}") {
		t.Error("metadata does not publish the full commit SHA tag")
	}
	outputs := yamlMap(t, publish["outputs"], "jobs.publish-main.outputs")
	if yamlString(t, outputs["image-digest"], "image digest output") != "${{ steps.build.outputs.digest }}" {
		t.Error("publication digest output is not wired to the build digest")
	}
	for i, item := range append(yamlList(t, pr["steps"], "pr steps"), steps...) {
		step := yamlMap(t, item, fmt.Sprintf("all workflow step %d", i))
		if uses, ok := step["uses"]; ok && !regexp.MustCompile(`^[^@]+@[0-9a-f]{40}(?:\s+#.*)?$`).MatchString(yamlString(t, uses, fmt.Sprintf("step %d uses", i))) {
			t.Errorf("workflow action is not pinned to a full commit SHA: %q", uses)
		}
	}
}

func TestPrivateEnvTemplateOptionalConfigurationIsDisabled(t *testing.T) {
	values := map[string]string{}
	for _, line := range strings.Split(repoFile(t, "deploy/private.env.example"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			t.Fatalf("invalid env template line %q", line)
		}
		values[name] = value
	}
	for _, name := range []string{"PORTFOLIO_DASHBOARD_IMAGE", "PORTFOLIO_DATA_DIR", "PORTFOLIO_CONTAINER_UID", "PORTFOLIO_CONTAINER_GID", "PORTFOLIO_HOST_PORT", "PORTFOLIO_REPORTING_CURRENCY"} {
		if _, ok := values[name]; !ok {
			t.Errorf("missing Compose interpolation setting %s", name)
		}
	}
	if values["PORTFOLIO_DASHBOARD_IMAGE"] != "" {
		t.Errorf("optional PORTFOLIO_DASHBOARD_IMAGE is not blank")
	}
	for _, name := range []string{"PORTFOLIO_MACHINE_PRINCIPAL_ID", "PORTFOLIO_MACHINE_PORTFOLIO_ID", "PORTFOLIO_MACHINE_BEARER_SECRET", "FINNHUB_API_KEY", "ALPHA_VANTAGE_API_KEY", "OPEN_EXCHANGE_RATES_APP_ID"} {
		if values[name] != "" {
			t.Errorf("optional %s is not blank", name)
		}
	}
	for _, name := range []string{"PORTFOLIO_MACHINE_PORTFOLIO_READ", "PORTFOLIO_MACHINE_REVIEWS_WRITE"} {
		if values[name] != "false" {
			t.Errorf("optional %s = %q, want false", name, values[name])
		}
	}
}

func TestRootPrivateEnvIsGitIgnored(t *testing.T) {
	command := exec.Command("git", "check-ignore", "--no-index", "--quiet", "private.env")
	command.Dir = repoRoot(t)
	if err := command.Run(); err != nil {
		t.Fatalf("git does not ignore root private.env: %v", err)
	}
}
