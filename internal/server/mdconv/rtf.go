package mdconv

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// skipDest are RTF destinations whose contents are not body text.
var skipDest = map[string]bool{
	"fonttbl": true, "colortbl": true, "stylesheet": true, "info": true, "pict": true,
	"header": true, "footer": true, "headerl": true, "headerr": true, "footerl": true,
	"footerr": true, "object": true, "fldinst": true, "themedata": true, "datastore": true,
}

// rtfMD extracts the text of an RTF document: paragraphs become blank-line
// separated blocks and bullets stay as text. Shortcut: \'hh bytes are read as
// Latin-1 (not the declared \ansicpg codepage); upgrade if non-Western RTF
// matters. Tables and styling are flattened.
func rtfMD(data []byte) (string, error) {
	s := string(data)
	if !strings.HasPrefix(strings.TrimSpace(s), "{\\rtf") {
		return "", errNotRTF
	}
	var sb strings.Builder
	var skip []bool // per open group: is its content skipped?
	skipping := func() bool { return len(skip) > 0 && skip[len(skip)-1] }
	uc := 1 // \ucN: fallback chars to drop after each \u
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '{':
			skip = append(skip, skipping())
		case '}':
			if len(skip) > 0 {
				skip = skip[:len(skip)-1]
			}
		case '\r', '\n':
		case '\\':
			i++
			if i >= len(s) {
				break
			}
			switch n := s[i]; {
			case n == '\\' || n == '{' || n == '}':
				if !skipping() {
					sb.WriteByte(n)
				}
			case n == '~':
				if !skipping() {
					sb.WriteByte(' ')
				}
			case n == '\'':
				if i+2 < len(s) {
					if v, err := strconv.ParseUint(s[i+1:i+3], 16, 8); err == nil && !skipping() {
						sb.WriteRune(rune(v))
					}
					i += 2
				}
			case n == '*':
				if len(skip) > 0 {
					skip[len(skip)-1] = true // \* marks an ignorable destination
				}
			case n >= 'a' && n <= 'z' || n >= 'A' && n <= 'Z':
				j := i
				for j < len(s) && (s[j] >= 'a' && s[j] <= 'z' || s[j] >= 'A' && s[j] <= 'Z') {
					j++
				}
				word := s[i:j]
				k := j
				if k < len(s) && s[k] == '-' {
					k++
				}
				for k < len(s) && s[k] >= '0' && s[k] <= '9' {
					k++
				}
				param, hasParam := 0, k > j
				if hasParam {
					param, _ = strconv.Atoi(s[j:k])
				}
				if k < len(s) && s[k] == ' ' { // the delimiter space is not text
					k++
				}
				i = k - 1
				if skipDest[word] && len(skip) > 0 {
					skip[len(skip)-1] = true
				}
				if skipping() {
					break
				}
				switch word {
				case "par", "sect", "page":
					sb.WriteString("\n\n")
				case "line":
					sb.WriteString("\n")
				case "tab":
					sb.WriteString("\t")
				case "emdash":
					sb.WriteString("—")
				case "endash":
					sb.WriteString("–")
				case "bullet":
					sb.WriteString("•")
				case "uc":
					uc = param
				case "u":
					if param < 0 {
						param += 65536
					}
					sb.WriteRune(rune(param))
					for d := 0; d < uc && i+1 < len(s); d++ { // skip the ASCII fallback
						if s[i+1] == '\\' && i+2 < len(s) && s[i+2] == '\'' {
							i += 4
						} else {
							_, sz := utf8.DecodeRuneInString(s[i+1:])
							i += sz
						}
					}
				}
			}
		default:
			if !skipping() {
				sb.WriteByte(c)
			}
		}
	}
	// Collapse the runs of blank lines that empty paragraphs leave behind.
	out := strings.Split(strings.ReplaceAll(sb.String(), "\r", ""), "\n")
	var lines []string
	blank := 0
	for _, l := range out {
		if strings.TrimSpace(l) == "" {
			if blank++; blank > 1 {
				continue
			}
			l = ""
		} else {
			blank = 0
		}
		lines = append(lines, strings.TrimRight(l, " "))
	}
	return strings.TrimSpace(strings.Join(lines, "\n")), nil
}
