package pdfextract

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"github.com/klippa-app/go-pdfium/enums"
	"github.com/klippa-app/go-pdfium/requests"
	"github.com/klippa-app/go-pdfium/structs"
)

// Mark is one annotation request for Annotate.
type Mark struct {
	Kind    string  // highlight | underline | strikeout | squiggly | box | note
	Text    string  // phrase to mark (every match, case-insensitive); empty for a note at X/Y
	Page    int     // 1-based; 0 = search every page (a note without Text needs a page)
	Nth     int     // 0 = every match; n = only the nth match in page order
	Comment string  // text of the comment attached to the mark
	Color   string  // name (yellow, green, blue, pink, red, orange, purple) or #RRGGBB
	X, Y    float64 // note without Text: position in points from the page's top-left corner
	Author  string
}

// MarkResult is what became of one Mark.
type MarkResult struct {
	Applied int    // annotations created
	Pages   []int  // pages they are on
	Problem string // why nothing (or not everything) was done
}

// MaxMatchesPerMark bounds one phrase's annotations: a phrase like "the" would
// otherwise bury the document.
const MaxMatchesPerMark = 500

var markColors = map[string][3]uint{
	"yellow": {255, 235, 0}, "green": {120, 230, 90}, "blue": {100, 170, 255},
	"pink": {255, 130, 200}, "red": {235, 40, 40}, "orange": {255, 160, 30}, "purple": {170, 110, 230},
}

var defaultColor = map[string]string{
	"highlight": "yellow", "underline": "blue", "strikeout": "red", "squiggly": "red", "box": "red", "note": "yellow",
}

func parseColor(s, kind string) ([3]uint, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		s = defaultColor[kind]
	}
	if c, ok := markColors[s]; ok {
		return c, nil
	}
	if h := strings.TrimPrefix(s, "#"); len(h) == 6 {
		if v, err := strconv.ParseUint(h, 16, 32); err == nil {
			return [3]uint{uint(v >> 16 & 255), uint(v >> 8 & 255), uint(v & 255)}, nil
		}
	}
	return [3]uint{}, fmt.Errorf("unknown color %q (use yellow, green, blue, pink, red, orange, purple or #RRGGBB)", s)
}

var markSubtypes = map[string]enums.FPDF_ANNOTATION_SUBTYPE{
	"highlight": enums.FPDF_ANNOT_SUBTYPE_HIGHLIGHT,
	"underline": enums.FPDF_ANNOT_SUBTYPE_UNDERLINE,
	"strikeout": enums.FPDF_ANNOT_SUBTYPE_STRIKEOUT,
	"squiggly":  enums.FPDF_ANNOT_SUBTYPE_SQUIGGLY,
	"box":       enums.FPDF_ANNOT_SUBTYPE_SQUARE,
	"note":      enums.FPDF_ANNOT_SUBTYPE_TEXT,
}

// Annotate adds the marks to the PDF and returns the new file, one result per
// mark. The original text and pages are untouched: marks are standard PDF
// annotations that every viewer shows (and can edit or delete). It fails as a
// whole only when the file cannot be read or written; a phrase that is not
// found is reported in its result.
func (e *Extractor) Annotate(data []byte, marks []Mark) ([]byte, []MarkResult, error) {
	mu.Lock()
	defer mu.Unlock()

	inst, err := e.pool.GetInstance(instanceTimeout)
	if err != nil {
		return nil, nil, fmt.Errorf("pdfextract: get instance: %w", err)
	}
	defer inst.Close()
	doc, err := inst.OpenDocument(&requests.OpenDocument{File: &data})
	if err != nil {
		return nil, nil, fmt.Errorf("pdfextract: open document: %w", err)
	}
	defer inst.FPDF_CloseDocument(&requests.FPDF_CloseDocument{Document: doc.Document})
	pc, err := inst.FPDF_GetPageCount(&requests.FPDF_GetPageCount{Document: doc.Document})
	if err != nil {
		return nil, nil, fmt.Errorf("pdfextract: page count: %w", err)
	}
	page := func(i int) requests.Page {
		return requests.Page{ByIndex: &requests.PageByIndex{Document: doc.Document, Index: i}}
	}

	// add creates one annotation of the mark's kind over rects (page coordinates,
	// y up) and attaches the comment.
	add := func(m Mark, rgb [3]uint, pageIdx int, rects []structs.FPDF_FS_RECTF) error {
		a, err := inst.FPDFPage_CreateAnnot(&requests.FPDFPage_CreateAnnot{Page: page(pageIdx), Subtype: markSubtypes[m.Kind]})
		if err != nil {
			return err
		}
		defer inst.FPDFPage_CloseAnnot(&requests.FPDFPage_CloseAnnot{Annotation: a.Annotation})
		box := rects[0]
		for _, r := range rects[1:] {
			box.Left, box.Right = min(box.Left, r.Left), max(box.Right, r.Right)
			box.Top, box.Bottom = max(box.Top, r.Top), min(box.Bottom, r.Bottom)
		}
		if m.Kind == "box" { // glyph rects hug the letters; a frame drawn on them reads as a strike-through
			const pad = 3
			box.Left, box.Right, box.Top, box.Bottom = box.Left-pad, box.Right+pad, box.Top+pad, box.Bottom-pad
		}
		if _, err := inst.FPDFAnnot_SetRect(&requests.FPDFAnnot_SetRect{Annotation: a.Annotation, Rect: box}); err != nil {
			return err
		}
		if _, err := inst.FPDFAnnot_SetColor(&requests.FPDFAnnot_SetColor{
			Annotation: a.Annotation, ColorType: enums.FPDFANNOT_COLORTYPE_Color, R: rgb[0], G: rgb[1], B: rgb[2], A: 255,
		}); err != nil {
			return err
		}
		switch m.Kind {
		case "highlight", "underline", "strikeout", "squiggly":
			for _, r := range rects {
				q := structs.FPDF_FS_QUADPOINTSF{X1: r.Left, Y1: r.Top, X2: r.Right, Y2: r.Top, X3: r.Left, Y3: r.Bottom, X4: r.Right, Y4: r.Bottom}
				if _, err := inst.FPDFAnnot_AppendAttachmentPoints(&requests.FPDFAnnot_AppendAttachmentPoints{Annotation: a.Annotation, AttachmentPoints: q}); err != nil {
					return err
				}
			}
		case "box":
			inst.FPDFAnnot_SetBorder(&requests.FPDFAnnot_SetBorder{Annotation: a.Annotation, BorderWidth: 1.5})
		}
		if m.Comment != "" {
			if _, err := inst.FPDFAnnot_SetStringValue(&requests.FPDFAnnot_SetStringValue{Annotation: a.Annotation, Key: "Contents", Value: m.Comment}); err != nil {
				return err
			}
		}
		if m.Author != "" {
			inst.FPDFAnnot_SetStringValue(&requests.FPDFAnnot_SetStringValue{Annotation: a.Annotation, Key: "T", Value: m.Author})
		}
		inst.FPDFAnnot_SetFlags(&requests.FPDFAnnot_SetFlags{Annotation: a.Annotation, Flags: enums.FPDF_ANNOT_FLAG_PRINT})
		return nil
	}

	results := make([]MarkResult, len(marks))
	for mi, m := range marks {
		res := &results[mi]
		m.Kind = strings.ToLower(strings.TrimSpace(m.Kind))
		if _, ok := markSubtypes[m.Kind]; !ok {
			res.Problem = fmt.Sprintf("unknown kind %q (use highlight, underline, strikeout, squiggly, box or note)", m.Kind)
			continue
		}
		rgb, err := parseColor(m.Color, m.Kind)
		if err != nil {
			res.Problem = err.Error()
			continue
		}
		if m.Page < 0 || m.Page > pc.PageCount {
			res.Problem = fmt.Sprintf("page %d does not exist (the document has %d pages)", m.Page, pc.PageCount)
			continue
		}
		pages := []int{}
		if m.Page > 0 {
			pages = append(pages, m.Page-1)
		} else if strings.TrimSpace(m.Text) != "" {
			for i := 0; i < pc.PageCount; i++ {
				pages = append(pages, i)
			}
		}

		// A note at a position (no phrase).
		if strings.TrimSpace(m.Text) == "" {
			if m.Kind != "note" {
				res.Problem = m.Kind + " needs text: the phrase to mark"
				continue
			}
			if m.Page == 0 {
				res.Problem = "a note without text needs a page (and x, y in points from the top-left corner)"
				continue
			}
			h, err := inst.FPDF_GetPageHeightF(&requests.FPDF_GetPageHeightF{Page: page(m.Page - 1)})
			if err != nil {
				res.Problem = err.Error()
				continue
			}
			top := float32(h.PageHeight) - float32(m.Y)
			r := structs.FPDF_FS_RECTF{Left: float32(m.X), Right: float32(m.X) + 20, Top: top, Bottom: top - 20}
			if err := add(m, rgb, m.Page-1, []structs.FPDF_FS_RECTF{r}); err != nil {
				res.Problem = err.Error()
				continue
			}
			res.Applied, res.Pages = 1, []int{m.Page}
			continue
		}

		seen := 0
	pagesLoop:
		for _, pi := range pages {
			tp, err := inst.FPDFText_LoadPage(&requests.FPDFText_LoadPage{Page: page(pi)})
			if err != nil {
				continue
			}
			sch, err := inst.FPDFText_FindStart(&requests.FPDFText_FindStart{TextPage: tp.TextPage, Find: m.Text, StartIndex: 0})
			if err != nil {
				inst.FPDFText_ClosePage(&requests.FPDFText_ClosePage{TextPage: tp.TextPage})
				continue
			}
			for {
				nx, err := inst.FPDFText_FindNext(&requests.FPDFText_FindNext{Search: sch.Search})
				if err != nil || !nx.GotMatch {
					break
				}
				seen++
				if m.Nth > 0 && seen != m.Nth {
					continue
				}
				idx, _ := inst.FPDFText_GetSchResultIndex(&requests.FPDFText_GetSchResultIndex{Search: sch.Search})
				cnt, _ := inst.FPDFText_GetSchCount(&requests.FPDFText_GetSchCount{Search: sch.Search})
				nr, err := inst.FPDFText_CountRects(&requests.FPDFText_CountRects{TextPage: tp.TextPage, StartIndex: idx.Index, Count: cnt.Count})
				if err != nil || nr.Count <= 0 {
					continue
				}
				var rects []structs.FPDF_FS_RECTF
				for k := 0; k < nr.Count; k++ {
					r, err := inst.FPDFText_GetRect(&requests.FPDFText_GetRect{TextPage: tp.TextPage, Index: k})
					if err != nil {
						continue
					}
					rects = append(rects, structs.FPDF_FS_RECTF{Left: float32(r.Left), Top: float32(r.Top), Right: float32(r.Right), Bottom: float32(r.Bottom)})
				}
				if len(rects) == 0 {
					continue
				}
				if m.Kind == "note" { // the icon sits at the start of the match
					rects = []structs.FPDF_FS_RECTF{{Left: rects[0].Left, Right: rects[0].Left + 20, Top: rects[0].Top + 2, Bottom: rects[0].Top - 18}}
				}
				if err := add(m, rgb, pi, rects); err != nil {
					res.Problem = err.Error()
					break pagesLoop
				}
				res.Applied++
				if n := len(res.Pages); n == 0 || res.Pages[n-1] != pi+1 {
					res.Pages = append(res.Pages, pi+1)
				}
				if res.Applied >= MaxMatchesPerMark {
					res.Problem = fmt.Sprintf("stopped after %d matches; use a longer phrase, a page, or nth", MaxMatchesPerMark)
					inst.FPDFText_FindClose(&requests.FPDFText_FindClose{Search: sch.Search})
					inst.FPDFText_ClosePage(&requests.FPDFText_ClosePage{TextPage: tp.TextPage})
					break pagesLoop
				}
			}
			inst.FPDFText_FindClose(&requests.FPDFText_FindClose{Search: sch.Search})
			inst.FPDFText_ClosePage(&requests.FPDFText_ClosePage{TextPage: tp.TextPage})
		}
		if res.Applied == 0 && res.Problem == "" {
			res.Problem = fmt.Sprintf("%q was not found", m.Text)
			if m.Nth > 0 && seen > 0 {
				res.Problem = fmt.Sprintf("%q occurs only %d time(s), so there is no match number %d", m.Text, seen, m.Nth)
			}
		}
	}

	var buf bytes.Buffer
	if _, err := inst.FPDF_SaveAsCopy(&requests.FPDF_SaveAsCopy{Document: doc.Document, FileWriter: &buf}); err != nil {
		return nil, nil, fmt.Errorf("pdfextract: save: %w", err)
	}
	return buf.Bytes(), results, nil
}
