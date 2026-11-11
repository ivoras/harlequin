// Package ocrvision transcribes scanned PDFs with a vision-capable chat model:
// each page is rasterised and sent as an image. It backs up docling's OCR for
// photographed/faded documents, at the price of per-page model cost and sending
// the pages to the model's provider.
package ocrvision

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ivoras/harlequin/internal/server/llm"
)

const (
	dpi         = 150
	concurrency = 3
	pageTimeout = 2 * time.Minute
	// MaxPages bounds cost: shortcut: pages beyond this are not read; raise or
	// make configurable if long scanned documents matter.
	MaxPages = 30
	prompt   = "Transcribe all text on this page exactly as printed (OCR). Keep the reading order and line breaks. Output only the text, no commentary."
)

// Reader OCRs PDFs through Model on Provider. Render rasterises pages to JPEG
// (pdfextract.Extractor.RenderPages).
type Reader struct {
	Provider llm.Provider
	Model    string
	Render   func(data []byte, dpi, maxPages int) ([][]byte, int, error)
	// Context is prepended to the prompt (see OrgContext); may be empty.
	Context string
}

// OrgContext builds the prompt context from the org name and description
// ("" when there is no description). The description may be in any language.
func OrgContext(orgName, description string) string {
	description = strings.TrimSpace(description)
	if description == "" {
		return ""
	}
	return fmt.Sprintf("Organisation: %s. %s", strings.TrimSpace(orgName), description)
}

// PDF returns the transcribed text and the rune offset where each page starts.
func (r *Reader) PDF(ctx context.Context, filename string, data []byte) (string, []int, error) {
	imgs, total, err := r.Render(data, dpi, MaxPages)
	if err != nil {
		return "", nil, err
	}
	if len(imgs) == 0 {
		return "", nil, fmt.Errorf("ocrvision: no pages")
	}
	texts := make([]string, len(imgs))
	errs := make([]error, len(imgs))
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i := range imgs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			texts[i], errs[i] = r.page(ctx, imgs[i])
		}()
	}
	wg.Wait()
	for i, e := range errs {
		if e != nil {
			return "", nil, fmt.Errorf("ocrvision: page %d of %q: %w", i+1, filename, e)
		}
	}
	var sb strings.Builder
	var starts []int
	off := 0
	for i, t := range texts {
		if i > 0 {
			sb.WriteString("\n\n")
			off += 2
		}
		starts = append(starts, off)
		sb.WriteString(t)
		off += utf8.RuneCountInString(t)
	}
	if strings.TrimSpace(sb.String()) == "" {
		return "", nil, fmt.Errorf("ocrvision: model returned no text")
	}
	_ = total // pages beyond MaxPages are dropped silently by Render
	return sb.String(), starts, nil
}

func (r *Reader) page(ctx context.Context, jpg []byte) (string, error) {
	// Reasoning models can loop on a page; a failed page fails the document so
	// docling's OCR takes over.
	ctx, cancel := context.WithTimeout(ctx, pageTimeout)
	defer cancel()
	zero := 0.0
	ch, err := r.Provider.Chat(ctx, llm.ChatRequest{
		Model:           r.Model,
		Temperature:     &zero,
		DisableThinking: true,
		Messages: []llm.Message{{Role: llm.RoleUser, Content: strings.TrimSpace(r.Context + " " + prompt),
			Images: []llm.Image{{Mime: "image/jpeg", Data: jpg}}}},
	})
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	for c := range ch {
		if c.Err != nil {
			return "", c.Err
		}
		sb.WriteString(c.TextDelta)
	}
	return strings.TrimSpace(sb.String()), nil
}
