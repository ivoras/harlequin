// Package pdfextract extracts plain text from PDF bytes using PDFium via the
// pure-Go (wazero/WebAssembly) backend of go-pdfium — no cgo, no native library
// to install, and parsing runs in a wasm sandbox so a malformed/untrusted upload
// can't crash or exploit the host process.
package pdfextract

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/webassembly"
)

// Extractor owns a PDFium worker pool. It is safe for concurrent use; the pool
// serialises access to the single-threaded wasm runtime. Construct once and reuse.
type Extractor struct {
	pool pdfium.Pool
}

// New initialises the wasm PDFium pool. Close it on shutdown.
func New() (*Extractor, error) {
	pool, err := webassembly.Init(webassembly.Config{
		MinIdle:  1,
		MaxIdle:  1,
		MaxTotal: 1,
	})
	if err != nil {
		return nil, fmt.Errorf("pdfextract: init wasm pdfium: %w", err)
	}
	return &Extractor{pool: pool}, nil
}

// Close releases the worker pool.
func (e *Extractor) Close() error {
	if e == nil || e.pool == nil {
		return nil
	}
	return e.pool.Close()
}

// instanceTimeout bounds how long we wait for a free worker from the pool.
const instanceTimeout = 30 * time.Second

// mu serialises document operations: the wasm runtime is single-threaded and the
// pool has one instance, so concurrent extractions must not interleave.
var mu sync.Mutex

// Text extracts the plain text of every page, joined with blank lines. It returns
// the text and the page count.
// Pages extracts the text of each page separately (so the caller can map chunks
// back to a page). The concatenation of the result, joined by "\n\n", equals
// Text's output.
func (e *Extractor) Pages(data []byte) ([]string, error) {
	mu.Lock()
	defer mu.Unlock()

	inst, err := e.pool.GetInstance(instanceTimeout)
	if err != nil {
		return nil, fmt.Errorf("pdfextract: get instance: %w", err)
	}
	defer inst.Close()
	doc, err := inst.OpenDocument(&requests.OpenDocument{File: &data})
	if err != nil {
		return nil, fmt.Errorf("pdfextract: open document: %w", err)
	}
	defer inst.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: doc.Document})
	pc, err := inst.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: doc.Document})
	if err != nil {
		return nil, fmt.Errorf("pdfextract: page count: %w", err)
	}
	pages := make([]string, 0, pc.PageCount)
	for i := 0; i < pc.PageCount; i++ {
		pt, err := inst.GetPageText(&requests.GetPageText{
			Page: requests.Page{ByIndex: &requests.PageByIndex{Document: doc.Document, Index: i}},
		})
		if err != nil {
			return nil, fmt.Errorf("pdfextract: page %d text: %w", i, err)
		}
		pages = append(pages, pt.Text)
	}
	return pages, nil
}

func (e *Extractor) Text(data []byte) (string, int, error) {
	mu.Lock()
	defer mu.Unlock()

	inst, err := e.pool.GetInstance(instanceTimeout)
	if err != nil {
		return "", 0, fmt.Errorf("pdfextract: get instance: %w", err)
	}
	defer inst.Close()

	doc, err := inst.OpenDocument(&requests.OpenDocument{File: &data})
	if err != nil {
		return "", 0, fmt.Errorf("pdfextract: open document: %w", err)
	}
	defer inst.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: doc.Document})

	pc, err := inst.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: doc.Document})
	if err != nil {
		return "", 0, fmt.Errorf("pdfextract: page count: %w", err)
	}

	var sb strings.Builder
	for i := 0; i < pc.PageCount; i++ {
		pt, err := inst.GetPageText(&requests.GetPageText{
			Page: requests.Page{ByIndex: &requests.PageByIndex{Document: doc.Document, Index: i}},
		})
		if err != nil {
			return "", pc.PageCount, fmt.Errorf("pdfextract: page %d text: %w", i, err)
		}
		if i > 0 {
			sb.WriteString("\n\n")
		}
		sb.WriteString(pt.Text)
	}
	return sb.String(), pc.PageCount, nil
}
