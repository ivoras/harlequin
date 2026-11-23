package sheetdb

import (
	"archive/zip"
	"encoding/xml"
	"fmt"
	"math"
	"strconv"
	"strings"
)

// readTheme returns the theme colours as hex in the order a fill's theme
// index uses: lt1, dk1, lt2, dk2, accent1–6, hlink, folHlink (the file lists
// dk1 before lt1, and dk2 before lt2).
func readTheme(f *zip.File) []string {
	b, err := readAll(f)
	if err != nil {
		return nil
	}
	dec := xml.NewDecoder(strings.NewReader(string(b)))
	var order []string
	colors := map[string]string{}
	cur, in := "", false
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "clrScheme":
				in = true
			case "dk1", "lt1", "dk2", "lt2", "accent1", "accent2", "accent3", "accent4", "accent5", "accent6", "hlink", "folHlink":
				if in {
					cur = t.Name.Local
					order = append(order, cur)
				}
			case "srgbClr", "sysClr":
				for _, a := range t.Attr {
					if cur != "" && (a.Name.Local == "val" && t.Name.Local == "srgbClr" || a.Name.Local == "lastClr") {
						colors[cur] = a.Value
					}
				}
			}
		case xml.EndElement:
			if t.Name.Local == "clrScheme" {
				in = false
			}
		}
	}
	idx := []string{"lt1", "dk1", "lt2", "dk2", "accent1", "accent2", "accent3", "accent4", "accent5", "accent6", "hlink", "folHlink"}
	out := make([]string, len(idx))
	for i, k := range idx {
		out[i] = colors[k]
	}
	return out
}

// indexedColors is the legacy 64-colour palette fills may refer to by index.
var indexedColors = strings.Fields(`000000 FFFFFF FF0000 00FF00 0000FF FFFF00 FF00FF 00FFFF
000000 FFFFFF FF0000 00FF00 0000FF FFFF00 FF00FF 00FFFF 800000 008000 000080 808000 800080 008080
C0C0C0 808080 9999FF 993366 FFFFCC CCFFFF 660066 FF8080 0066CC CCCCFF 000080 FF00FF FFFF00 00FFFF
800080 800000 008080 0000FF 00CCFF CCFFFF CCFFCC FFFF99 99CCFF FF99CC CC99FF FFCC99 3366FF 33CCCC
99CC00 FFCC00 FF9900 FF6600 666699 969696 003366 339966 003300 333300 993300 993366 333399 333333`)

// colorWords names a fill colour the way a person would ("light blue"),
// with its hex code: users say "the blue rows", models need a word to match.
// White and unresolvable colours give "" (no fill as far as a reader sees).
func colorWords(hex string, tint float64) string {
	if len(hex) != 6 {
		return ""
	}
	v, err := strconv.ParseUint(hex, 16, 32)
	if err != nil {
		return ""
	}
	r, g, b := float64(v>>16&255)/255, float64(v>>8&255)/255, float64(v&255)/255
	h, s, l := toHSL(r, g, b)
	// Excel's tint moves lightness towards white (>0) or black (<0).
	if tint > 0 {
		l = l*(1-tint) + tint
	} else if tint < 0 {
		l *= 1 + tint
	}
	r, g, b = fromHSL(h, s, l)
	hexOut := fmt.Sprintf("#%02X%02X%02X", int(math.Round(r*255)), int(math.Round(g*255)), int(math.Round(b*255)))
	if l > 0.97 {
		return "" // white
	}
	var name string
	switch {
	case s < 0.15 && l < 0.2:
		name = "black"
	case s < 0.15:
		name = "grey"
	case h < 15 || h >= 345:
		name = "red"
	case h < 45:
		name = "orange"
	case h < 70:
		name = "yellow"
	case h < 170:
		name = "green"
	case h < 200:
		name = "turquoise"
	case h < 255:
		name = "blue"
	case h < 300:
		name = "purple"
	default:
		name = "pink"
	}
	if name != "black" {
		switch {
		case l > 0.75:
			name = "light " + name
		case l < 0.3:
			name = "dark " + name
		}
	}
	return name + " " + hexOut
}

func toHSL(r, g, b float64) (h, s, l float64) {
	mx, mn := math.Max(r, math.Max(g, b)), math.Min(r, math.Min(g, b))
	l = (mx + mn) / 2
	if mx == mn {
		return 0, 0, l
	}
	d := mx - mn
	if l > 0.5 {
		s = d / (2 - mx - mn)
	} else {
		s = d / (mx + mn)
	}
	switch mx {
	case r:
		h = math.Mod((g-b)/d, 6)
	case g:
		h = (b-r)/d + 2
	default:
		h = (r-g)/d + 4
	}
	h *= 60
	if h < 0 {
		h += 360
	}
	return h, s, l
}

func fromHSL(h, s, l float64) (r, g, b float64) {
	c := (1 - math.Abs(2*l-1)) * s
	x := c * (1 - math.Abs(math.Mod(h/60, 2)-1))
	m := l - c/2
	switch {
	case h < 60:
		r, g, b = c, x, 0
	case h < 120:
		r, g, b = x, c, 0
	case h < 180:
		r, g, b = 0, c, x
	case h < 240:
		r, g, b = 0, x, c
	case h < 300:
		r, g, b = x, 0, c
	default:
		r, g, b = c, 0, x
	}
	return r + m, g + m, b + m
}
