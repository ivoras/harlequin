// Package pdfextract extracts plain text from PDF bytes using PDFium via the
// pure-Go (wazero/WebAssembly) backend of go-pdfium — no cgo, no native library
// to install, and parsing runs in a wasm sandbox so a malformed/untrusted upload
// can't crash or exploit the host process.
package pdfextract

import (
	"bytes"
	"fmt"
	"image/jpeg"
	"strings"
	"sync"
	"time"

	"github.com/klippa-app/go-pdfium"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/responses"
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

// RenderPages rasterises up to maxPages pages as JPEGs (for a vision model),
// returning them in page order and the document's total page count.
func (e *Extractor) RenderPages(data []byte, dpi, maxPages int) ([][]byte, int, error) {
	mu.Lock()
	defer mu.Unlock()

	inst, err := e.pool.GetInstance(instanceTimeout)
	if err != nil {
		return nil, 0, fmt.Errorf("pdfextract: get instance: %w", err)
	}
	defer inst.Close()
	doc, err := inst.OpenDocument(&requests.OpenDocument{File: &data})
	if err != nil {
		return nil, 0, fmt.Errorf("pdfextract: open document: %w", err)
	}
	defer inst.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: doc.Document})
	pc, err := inst.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: doc.Document})
	if err != nil {
		return nil, 0, fmt.Errorf("pdfextract: page count: %w", err)
	}
	var out [][]byte
	for i := 0; i < pc.PageCount && i < maxPages; i++ {
		r, err := inst.RenderPageInDPI(&requests.RenderPageInDPI{
			DPI:  dpi,
			Page: requests.Page{ByIndex: &requests.PageByIndex{Document: doc.Document, Index: i}},
		})
		if err != nil {
			return nil, 0, fmt.Errorf("pdfextract: render page %d: %w", i+1, err)
		}
		var buf bytes.Buffer
		err = jpeg.Encode(&buf, r.Result.Image, &jpeg.Options{Quality: 85})
		r.Cleanup()
		if err != nil {
			return nil, 0, err
		}
		out = append(out, buf.Bytes())
	}
	return out, pc.PageCount, nil
}

// PagePart is a run of pages (1-based, in the given order) taken from one of
// the source PDFs handed to Assemble.
type PagePart struct {
	Source int
	Pages  []int
}

// PageCounts returns the number of pages of each PDF.
func (e *Extractor) PageCounts(sources [][]byte) ([]int, error) {
	mu.Lock()
	defer mu.Unlock()
	inst, err := e.pool.GetInstance(instanceTimeout)
	if err != nil {
		return nil, fmt.Errorf("pdfextract: get instance: %w", err)
	}
	defer inst.Close()
	counts := make([]int, len(sources))
	for i := range sources {
		doc, err := inst.OpenDocument(&requests.OpenDocument{File: &sources[i]})
		if err != nil {
			return nil, fmt.Errorf("pdfextract: open source %d: %w", i+1, err)
		}
		pc, err := inst.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: doc.Document})
		inst.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: doc.Document})
		if err != nil {
			return nil, fmt.Errorf("pdfextract: page count: %w", err)
		}
		counts[i] = pc.PageCount
	}
	return counts, nil
}

// Assemble builds one new PDF per entry of outputs by copying pages out of
// sources (extract, merge, reorder, split). Pages are copied as they are —
// content, layout and fonts are untouched — and each source is parsed once,
// however many outputs draw on it. Page numbers must exist (see PageCounts).
func (e *Extractor) Assemble(sources [][]byte, outputs [][]PagePart) ([][]byte, error) {
	mu.Lock()
	defer mu.Unlock()
	inst, err := e.pool.GetInstance(instanceTimeout)
	if err != nil {
		return nil, fmt.Errorf("pdfextract: get instance: %w", err)
	}
	defer inst.Close()
	docs := make([]*responses.OpenDocument, len(sources))
	for i := range sources {
		if docs[i], err = inst.OpenDocument(&requests.OpenDocument{File: &sources[i]}); err != nil {
			return nil, fmt.Errorf("pdfextract: open source %d: %w", i+1, err)
		}
		defer inst.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: docs[i].Document})
	}
	one := func(parts []PagePart) ([]byte, error) {
		dst, err := inst.FPDF_CreateNewDocument(&requests.FPDF_CreateNewDocument{})
		if err != nil {
			return nil, fmt.Errorf("pdfextract: new document: %w", err)
		}
		defer inst.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: dst.Document})
		n := 0
		for _, p := range parts {
			if p.Source < 0 || p.Source >= len(docs) {
				return nil, fmt.Errorf("pdfextract: no source %d", p.Source)
			}
			// One import per run of consecutive pages, so the order asked for is
			// the order produced whatever the library does inside a range list.
			for i := 0; i < len(p.Pages); {
				j := i
				for j+1 < len(p.Pages) && p.Pages[j+1] == p.Pages[j]+1 {
					j++
				}
				r := fmt.Sprintf("%d-%d", p.Pages[i], p.Pages[j])
				if _, err := inst.FPDF_ImportPages(&requests.FPDF_ImportPages{
					Source: docs[p.Source].Document, Destination: dst.Document, PageRange: &r, Index: n,
				}); err != nil {
					return nil, fmt.Errorf("pdfextract: import pages %s: %w", r, err)
				}
				n += j - i + 1
				i = j + 1
			}
		}
		if n == 0 {
			return nil, fmt.Errorf("pdfextract: no pages selected")
		}
		var buf bytes.Buffer
		if _, err := inst.FPDF_SaveAsCopy(&requests.FPDF_SaveAsCopy{Document: dst.Document, FileWriter: &buf}); err != nil {
			return nil, fmt.Errorf("pdfextract: save: %w", err)
		}
		return buf.Bytes(), nil
	}
	out := make([][]byte, len(outputs))
	for i, parts := range outputs {
		if out[i], err = one(parts); err != nil {
			return nil, err
		}
	}
	return out, nil
}
