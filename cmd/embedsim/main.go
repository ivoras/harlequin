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
	"strings"

	"github.com/ivoras/harlequin/internal/server/config"
	"github.com/ivoras/harlequin/internal/server/embed"
)

func main() {
	configPath := flag.String("config", "server.yaml", "path to server config YAML")
	urlFlag := flag.String("url", "", "embeddings base URL ending in /v1 (overrides server.yaml; e.g. http://127.0.0.1:2236/v1)")
	modelFlag := flag.String("model", "", "model name to send (used with -url)")
	flag.Parse()
	args := flag.Args()
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, `usage: embedsim [-config server.yaml | -url <…/v1> [-model M]] "<text a>" "<text b>"`)
		os.Exit(2)
	}

	var baseURL, model, apiKey string
	if *urlFlag != "" {
		baseURL = strings.TrimRight(*urlFlag, "/")
		model = *modelFlag
		apiKey = os.Getenv("EMBED_API_KEY")
	} else {
		cfg, err := config.Load(*configPath)
		if err != nil {
			fatal("config: %v", err)
		}
		if cfg.Embeddings.BaseURL == "" || cfg.Embeddings.Model == "" {
			fatal("no embeddings provider configured (set embeddings.base_url and embeddings.model, or pass -url)")
		}
		baseURL, model, apiKey = cfg.Embeddings.BaseURL, cfg.Embeddings.Model, cfg.Embeddings.APIKey
	}

	e := embed.New(baseURL, apiKey, model, 0, "", "")
	vecs, err := e.Embed(context.Background(), args)
	if err != nil {
		fatal("embed (%s @ %s): %v", model, baseURL, err)
	}
	if len(vecs) != 2 {
		fatal("expected 2 vectors, got %d", len(vecs))
	}
	a, b := vecs[0], vecs[1]
	if len(a) != len(b) {
		fatal("vector dimension mismatch: %d vs %d", len(a), len(b))
	}

	mName := model
	if mName == "" {
		mName = "(server default)"
	}
	fmt.Printf("model:  %s @ %s (dim %d)\n", mName, baseURL, len(a))
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
