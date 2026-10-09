package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"

	"github.com/boubonming/portfolio-dashboard/internal/config"
	"github.com/boubonming/portfolio-dashboard/internal/httpserver"
	"github.com/boubonming/portfolio-dashboard/internal/importer"
	"github.com/boubonming/portfolio-dashboard/internal/store"
)

func main() {
	if len(os.Args) > 1 {
		if err := runCommand(os.Args[1], os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		return
	}
	cfg := config.FromEnv()
	handler, err := httpserver.NewHandler()
	if err != nil {
		fatal(err)
	}
	fmt.Fprintf(os.Stderr, "portfolio dashboard listening on %s\n", cfg.BindAddress)
	fatal(http.ListenAndServe(cfg.BindAddress, handler))
}

func runCommand(command string, args []string) error {
	switch command {
	case "import-preview":
		return runPreview(args)
	case "import-apply":
		return runApply(args)
	case "import-status":
		return runStatus(args)
	default:
		return fmt.Errorf("unknown command %q (use import-preview, import-apply, or import-status)", command)
	}
}

func runPreview(args []string) error {
	flags := flag.NewFlagSet("import-preview", flag.ContinueOnError)
	input := flags.String("input", "", "authoritative Markdown input path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *input == "" {
		return errors.New("-input is required")
	}
	preview, err := importer.PreviewFile(*input)
	if err != nil {
		return err
	}
	return writeJSON(preview)
}

func runApply(args []string) error {
	flags := flag.NewFlagSet("import-apply", flag.ContinueOnError)
	input := flags.String("input", "", "authoritative Markdown input path")
	database := flags.String("db", "", "SQLite database path")
	expectedHash := flags.String("expected-sha256", "", "exact source SHA-256 from the approved preview")
	approved := flags.Bool("approve", false, "explicitly approve this exact preview hash for persistence")
	reportingCurrency := flags.String("reporting-currency", "MYR", "portfolio reporting currency")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *input == "" || *database == "" || *expectedHash == "" {
		return errors.New("-input, -db, and -expected-sha256 are required")
	}
	if !*approved {
		return errors.New("explicit approval is required: pass -approve")
	}
	preview, err := importer.PreviewFile(*input)
	if err != nil {
		return err
	}
	if preview.SourceSHA256 != *expectedHash {
		return fmt.Errorf("source hash mismatch: got %s, expected %s", preview.SourceSHA256, *expectedHash)
	}
	if err := importer.ValidateApproved(preview); err != nil {
		return err
	}
	ctx := context.Background()
	db, err := store.Open(ctx, *database)
	if err != nil {
		return err
	}
	defer db.Close()
	result, err := db.Apply(ctx, preview, *input, *expectedHash, *reportingCurrency)
	if err != nil {
		return err
	}
	return writeJSON(result)
}

func runStatus(args []string) error {
	flags := flag.NewFlagSet("import-status", flag.ContinueOnError)
	database := flags.String("db", "", "SQLite database path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *database == "" {
		return errors.New("-db is required")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, *database)
	if err != nil {
		return err
	}
	defer db.Close()
	status, err := db.Status(ctx)
	if err != nil {
		return err
	}
	return writeJSON(status)
}

func writeJSON(value any) error {
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func fatal(err error) {
	if err != nil {
		panic(err)
	}
}
