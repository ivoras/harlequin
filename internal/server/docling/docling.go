// Package docling is a minimal client for a docling-serve instance
// (https://github.com/docling-project/docling-serve): it converts uploaded
// documents (PDF, DOCX, PPTX, …) to structured Markdown via the synchronous
// POST /v1/convert/file endpoint. Docling is optional — when it is not
// configured or not reachable, uploads fall back to the built-in pure-Go
// extractors (PDFium-wasm for PDF, docxextract for DOCX), which extract plain
// text without Docling's layout analysis, table structure or OCR.
package docling

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

// DefaultTimeout bounds one conversion. Docling runs layout models per page,
// so large PDFs take a while; the fallback extractors remain available when a
// conversion times out.
const DefaultTimeout = 5 * time.Minute

// Client calls one docling-serve instance.
type Client struct {
	baseURL string
	hc      *http.Client
}

// New builds a client for the docling-serve at baseURL (e.g.
// "http://127.0.0.1:5001"). timeout <= 0 uses DefaultTimeout.
func New(baseURL string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		hc:      &http.Client{Timeout: timeout},
	}
}

// Healthy reports whether the instance answers its health check.
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

// convertResponse is the subset of docling-serve's response we use.
type convertResponse struct {
	Document struct {
		MDContent   string          `json:"md_content"`
		JSONContent json.RawMessage `json:"json_content"`
	} `json:"document"`
	Status string `json:"status"`
	Errors []any  `json:"errors"`
}

// taskStatus is docling-serve's async task envelope.
type taskStatus struct {
	TaskID     string `json:"task_id"`
	TaskStatus string `json:"task_status"` // pending | started | success | failure
	ErrorMsg   string `json:"error_message"`
}

// pollInterval paces async conversion status checks.
const pollInterval = 3 * time.Second

// convert runs one file through docling-serve's async API (the synchronous
// endpoint enforces its own internal time limit and 504s on long documents):
// submit, poll until terminal, fetch the result. The client timeout bounds the
// whole exchange via ctx.
func (c *Client) convert(ctx context.Context, filename string, data []byte, format string) (*convertResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, c.hc.Timeout)
	defer cancel()

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("files", filename)
	if err != nil {
		return nil, err
	}
	if _, err := fw.Write(data); err != nil {
		return nil, err
	}
	if err := mw.WriteField("to_formats", format); err != nil {
		return nil, err
	}
	if err := mw.Close(); err != nil {
		return nil, err
	}
	var task taskStatus
	if err := c.doJSON(ctx, http.MethodPost, "/v1/convert/file/async", &body, mw.FormDataContentType(), &task); err != nil {
		return nil, err
	}
	if task.TaskID == "" {
		return nil, fmt.Errorf("docling: async submit returned no task id")
	}
	for task.TaskStatus != "success" {
		if task.TaskStatus == "failure" {
			return nil, fmt.Errorf("docling: conversion failed: %s", task.ErrorMsg)
		}
		select {
		case <-time.After(pollInterval):
		case <-ctx.Done():
			return nil, fmt.Errorf("docling: conversion did not finish in time: %w", ctx.Err())
		}
		if err := c.doJSON(ctx, http.MethodGet, "/v1/status/poll/"+task.TaskID, nil, "", &task); err != nil {
			return nil, err
		}
	}
	var cr convertResponse
	if err := c.doJSON(ctx, http.MethodGet, "/v1/result/"+task.TaskID, nil, "", &cr); err != nil {
		return nil, err
	}
	if cr.Status != "success" {
		return nil, fmt.Errorf("docling: conversion status %q (%d errors)", cr.Status, len(cr.Errors))
	}
	return &cr, nil
}

// doJSON performs one HTTP exchange with a JSON response.
func (c *Client) doJSON(ctx context.Context, method, path string, body io.Reader, contentType string, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("docling: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("docling: %s %s: status %d: %s", method, path, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Convert sends one file and returns its Markdown rendering (no page mapping —
// use ConvertPaged for paged sources like PDF).
func (c *Client) Convert(ctx context.Context, filename string, data []byte) (string, error) {
	cr, err := c.convert(ctx, filename, data, "md")
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(cr.Document.MDContent) == "" {
		return "", fmt.Errorf("docling: conversion produced no text")
	}
	return cr.Document.MDContent, nil
}

// ConvertPaged converts a paged document via Docling's lossless JSON format and
// rebuilds Markdown-ish text in reading order, returning the rune offset at
// which each page begins (parallel to pdfextract's page mapping) so document
// chunks keep their page citations.
func (c *Client) ConvertPaged(ctx context.Context, filename string, data []byte) (string, []int, error) {
	cr, err := c.convert(ctx, filename, data, "json")
	if err != nil {
		return "", nil, err
	}
	if len(cr.Document.JSONContent) == 0 {
		return "", nil, fmt.Errorf("docling: conversion returned no json_content")
	}
	text, pageStarts, err := flattenDoclingJSON(cr.Document.JSONContent)
	if err != nil {
		return "", nil, err
	}
	if strings.TrimSpace(text) == "" {
		return "", nil, fmt.Errorf("docling: conversion produced no text")
	}
	return text, pageStarts, nil
}
