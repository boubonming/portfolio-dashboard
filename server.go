package main

import (
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"time"
)

// webFiles keeps the dashboard self-contained: no CDN or runtime network assets.
//
//go:embed web/index.html web/styles.css web/app.js
var webFiles embed.FS

func main() {
	address := ""
	dataPath := "portfolio.json"
	flagSet := newFlagSet(&address, &dataPath)
	if err := flagSet.Parse(os.Args[1:]); err != nil {
		log.Fatal(err)
	}
	snapshot, err := LoadPortfolio(dataPath)
	if err != nil {
		log.Fatal(err)
	}
	handler, err := NewHandler(snapshot)
	if err != nil {
		log.Fatal(err)
	}

	server := &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	log.Printf("portfolio dashboard listening on http://localhost%s (data: %s)", address, dataPath)
	log.Fatal(server.ListenAndServe())
}

// newFlagSet is split out so startup flag behavior is easy to test without globals.
func newFlagSet(address, dataPath *string) *flag.FlagSet {
	flags := flag.NewFlagSet("portfolio-dashboard", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(address, "addr", ":8080", "HTTP listen address")
	flags.StringVar(dataPath, "data", "portfolio.json", "portfolio snapshot JSON path")
	return flags
}

// NewHandler builds the complete HTTP application around one validated snapshot.
func NewHandler(snapshot PortfolioSnapshot) (http.Handler, error) {
	response, err := Calculate(snapshot)
	if err != nil {
		return nil, err
	}
	assets, err := fs.Sub(webFiles, "web")
	if err != nil {
		return nil, fmt.Errorf("embedded dashboard assets: %w", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/portfolio", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			methodNotAllowed(writer)
			return
		}
		writeJSON(writer, http.StatusOK, response)
	})
	mux.HandleFunc("/healthz", func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			methodNotAllowed(writer)
			return
		}
		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("healthy\n"))
	})
	mux.Handle("/", http.FileServer(http.FS(assets)))
	return mux, nil
}

func methodNotAllowed(writer http.ResponseWriter) {
	writer.Header().Set("Allow", http.MethodGet)
	http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		log.Printf("write JSON response: %v", err)
	}
}
