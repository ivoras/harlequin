// Package xberg is a minimal client for an xberg extraction server
// (https://github.com/xberg-io/xberg): it converts uploaded documents (PDF,
// DOCX, PPTX, …) to Markdown via the POST /extract endpoint. It is an
// alternative to the docling package — same two entry points (Convert,
// ConvertPaged), same page-mapping contract — backed by xberg's Rust core
// instead of docling-serve's Python one. Like docling it is optional: when it
// is not configured or not reachable, uploads fall back to the built-in pure-Go
// extractors (PDFium-wasm for PDF, docxextract for DOCX).
//
// Use cmd/extracttest to compare the two on real documents before switching.
package xberg

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"time"
)

// DefaultTimeout bounds one conversion. xberg's native text extraction is fast,
// but layout detection and OCR run models per page, so keep room for those.
const DefaultTimeout = 5 * time.Minute

// Client calls one xberg server.
type Client struct {
	baseURL string
	fast    bool
	hc      *http.Client
}

// New builds a client for the xberg server at baseURL (e.g.
// "http://127.0.0.1:8000"). timeout <= 0 uses DefaultTimeout. fast trades
// conversion quality for speed: it disables OCR and skips layout detection,
// which is much faster on digital-native documents but extracts nothing from
// scanned pages and recovers less structure (headings, tables) from complex
// layouts — the same trade-off as the docling client's fast flag.
func New(baseURL string, timeout time.Duration, fast bool) *Client {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		fast:    fast,
		hc:      &http.Client{Timeout: timeout},
	}
}

// Name identifies this converter in logs.
func (c *Client) Name() string { return "xberg" }

// Healthy reports whether the server answers its health check.
func (c *Client) Healthy(ctx context.Context) bool {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/health", nil)
	if err != nil {
		return false
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// extractResponse is the subset of xberg's /extract envelope we use: one
// per-document payload per input file.
type extractResponse struct {
	Results []extractedDoc `json:"results"`
}

type extractedDoc struct {
	Content  string `json:"content"`
	Metadata struct {
		Pages *struct {
			TotalCount int            `json:"total_count"`
			Boundaries []pageBoundary `json:"boundaries"`
			UnitType   string         `json:"unit_type"`
		} `json:"pages"`
	} `json:"metadata"`
	Counts struct {
		Pages  int `json:"pages"`
		Tables int `json:"tables"`
	} `json:"counts"`
	ExtractionMethod string `json:"extraction_method"`
}

// pageBoundary is xberg's byte range for one page of the content string.
type pageBoundary struct {
	ByteStart  int `json:"byte_start"`
	ByteEnd    int `json:"byte_end"`
	PageNumber int `json:"page_number"`
}

// extractionConfig is the subset of xberg's ExtractionConfig we send. The
// server rejects unknown fields (serde deny_unknown_fields), so every key here
// must match xberg's schema exactly — omitempty keeps unset options out of the
// payload rather than sending nulls.
type extractionConfig struct {
	OutputFormat string      `json:"output_format"`
	DisableOCR   bool        `json:"disable_ocr,omitempty"`
	Pages        *pageConfig `json:"pages,omitempty"`
	// Layout runs xberg's layout models (RT-DETR for regions, TATR for table
	// structure); UseLayoutForMarkdown feeds their output into the Markdown
	// renderer so headings and tables survive. This is the closest analogue to
	// what docling does by default, and the reason to run xberg non-fast.
	Layout               *layoutConfig `json:"layout,omitempty"`
	UseLayoutForMarkdown bool          `json:"use_layout_for_markdown,omitempty"`
}

type pageConfig struct {
	InsertPageMarkers bool   `json:"insert_page_markers"`
	MarkerFormat      string `json:"marker_format"`
}

type layoutConfig struct {
	Strategy string `json:"strategy"`
}

// config builds the extraction config for one conversion. paged requests the
// page markers that ConvertPaged turns into a page mapping; unpaged sources
// (DOCX) skip them so no markers can leak into the stored text.
func (c *Client) config(paged bool) extractionConfig {
	cfg := extractionConfig{OutputFormat: "markdown"}
	if c.fast {
		cfg.DisableOCR = true
	} else {
		// "auto" lets xberg gate the layout model per page, so digital-native
		// pages skip inference instead of paying for it.
		cfg.Layout = &layoutConfig{Strategy: "auto"}
		cfg.UseLayoutForMarkdown = true
	}
	if paged {
		cfg.Pages = &pageConfig{InsertPageMarkers: true, MarkerFormat: markerFormat}
	}
	return cfg
}

// extract sends one file to POST /extract and returns its payload.
func (c *Client) extract(ctx context.Context, filename string, data []byte, paged bool) (*extractedDoc, error) {
	ctx, cancel := context.WithTimeout(ctx, c.hc.Timeout)
	defer cancel()

	cfgJSON, err := json.Marshal(c.config(paged))
	if err != nil {
		return nil, err
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("files", filename)
	if err != nil {
		return nil, err
	}
	if _, err := fw.Write(data); err != nil {
		return nil, err
	}
	if err := mw.WriteField("config", string(cfgJSON)); err != nil {
		return nil, err
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/extract", &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Accept", "application/json")

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("xberg: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("xberg: POST /extract: status %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	var er extractResponse
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		return nil, fmt.Errorf("xberg: decode response: %w", err)
	}
	if len(er.Results) == 0 {
		return nil, fmt.Errorf("xberg: extraction returned no results")
	}
	return &er.Results[0], nil
}

// Convert sends one file and returns its Markdown rendering (no page mapping —
// use ConvertPaged for paged sources like PDF).
func (c *Client) Convert(ctx context.Context, filename string, data []byte) (string, error) {
	doc, err := c.extract(ctx, filename, data, false)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(doc.Content) == "" {
		return "", fmt.Errorf("xberg: conversion produced no text")
	}
	return doc.Content, nil
}

// ConvertPaged converts a paged document and returns its Markdown text plus the
// rune offset at which each page begins (parallel to pdfextract's page mapping)
// so document chunks keep their page citations.
//
// The mapping comes from the page markers xberg inserts, which are positions in
// the rendered Markdown and therefore align with the returned text by
// construction. When no markers survive the render, it falls back to the byte
// boundaries in metadata.pages — those are recorded over the extraction
// pipeline's content, so they can be slightly off on rendered output, but a
// slightly-off page citation beats none. A document with neither yields a nil
// mapping and unpaged chunks rather than an error: the text is still worth
// keeping.
func (c *Client) ConvertPaged(ctx context.Context, filename string, data []byte) (string, []int, error) {
	doc, err := c.extract(ctx, filename, data, true)
	if err != nil {
		return "", nil, err
	}
	text, pageStarts, ok := stripPageMarkers(doc.Content)
	if !ok && doc.Metadata.Pages != nil {
		pageStarts = boundaryStarts(text, doc.Metadata.Pages.Boundaries)
	}
	if strings.TrimSpace(text) == "" {
		return "", nil, fmt.Errorf("xberg: conversion produced no text")
	}
	return text, pageStarts, nil
}
