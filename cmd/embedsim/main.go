// Command embedsim embeds two strings via the server's currently configured
// embeddings provider and prints their cosine similarity. It reads the same
// server.yaml + .env as the server, so it uses exactly the configured model.
//
//	go run ./cmd/embedsim [-config server.yaml] "thoughtleaders.io" "Dinosaurs"
package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"os"

	"github.com/ivoras/harlequin/internal/server/config"
	"github.com/ivoras/harlequin/internal/server/embed"
)

func main() {
	configPath := flag.String("config", "server.yaml", "path to server config YAML")
	flag.Parse()
	args := flag.Args()
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, `usage: embedsim [-config server.yaml] "<text a>" "<text b>"`)
		os.Exit(2)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		fatal("config: %v", err)
	}
	if cfg.Embeddings.BaseURL == "" || cfg.Embeddings.Model == "" {
		fatal("no embeddings provider configured (set embeddings.base_url and embeddings.model)")
	}

	e := embed.New(cfg.Embeddings.BaseURL, cfg.Embeddings.APIKey, cfg.Embeddings.Model, cfg.Embeddings.Dim)
	vecs, err := e.Embed(context.Background(), args)
	if err != nil {
		fatal("embed (%s @ %s): %v", cfg.Embeddings.Model, cfg.Embeddings.BaseURL, err)
	}
	if len(vecs) != 2 {
		fatal("expected 2 vectors, got %d", len(vecs))
	}
	a, b := vecs[0], vecs[1]
	if len(a) != len(b) {
		fatal("vector dimension mismatch: %d vs %d", len(a), len(b))
	}

	fmt.Printf("model:  %s @ %s (dim %d)\n", cfg.Embeddings.Model, cfg.Embeddings.BaseURL, len(a))
	fmt.Printf("a:      %q\n", args[0])
	fmt.Printf("b:      %q\n", args[1])
	fmt.Printf("cosine: %.4f\n", cosine(a, b))
}

// cosine returns the cosine similarity of two vectors. It normalizes explicitly,
// so it is correct whether or not the provider already L2-normalizes its output.
func cosine(a, b []float32) float64 {
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "embedsim: "+format+"\n", a...)
	os.Exit(1)
}
