package render

import (
	"bytes"
	"os"
	"strings"
)

// Palette holds one SGR code per JSON element, in jq's JQ_COLORS order.
type Palette struct {
	Null, False, True, Number, String, Array, Object, Key string
}

// DefaultPalette is jq 1.7.1's default.
func DefaultPalette() Palette {
	return Palette{
		Null:   "1;30",
		False:  "0;39",
		True:   "0;39",
		Number: "0;39",
		String: "0;32",
		Array:  "1;39",
		Object: "1;39",
		Key:    "34;1",
	}
}

// ParsePalette applies a JQ_COLORS-style spec ("null:false:true:numbers:
// strings:arrays:objects:objkeys", each field an SGR code) over the defaults.
// A malformed spec is ignored as a whole, like jq does.
func ParsePalette(spec string) Palette {
	p := DefaultPalette()
	if spec == "" {
		return p
	}
	fields := strings.Split(spec, ":")
	if len(fields) > 8 {
		return p
	}
	for _, f := range fields {
		for _, r := range f {
			if (r < '0' || r > '9') && r != ';' {
				return p
			}
		}
	}
	targets := []*string{&p.Null, &p.False, &p.True, &p.Number, &p.String, &p.Array, &p.Object, &p.Key}
	for i, f := range fields {
		*targets[i] = f
	}
	return p
}

// PaletteFromEnv reads JQ_COLORS (or GOJQ_COLORS when set).
func PaletteFromEnv() Palette {
	if v := os.Getenv("GOJQ_COLORS"); v != "" {
		return ParsePalette(v)
	}
	return ParsePalette(os.Getenv("JQ_COLORS"))
}

// Colorize wraps the tokens of valid JSON (compact or indented) in SGR codes.
// Brackets, commas and colons take the colour of the container they belong to,
// as in jq.
func Colorize(src []byte, p Palette) []byte {
	var out bytes.Buffer
	out.Grow(len(src) * 2)
	var stack []string // colour of each open container

	paint := func(code string, b []byte) {
		if code == "" {
			out.Write(b)
			return
		}
		out.WriteString("\x1b[" + code + "m")
		out.Write(b)
		out.WriteString("\x1b[0m")
	}
	top := func() string {
		if len(stack) == 0 {
			return ""
		}
		return stack[len(stack)-1]
	}

	for i := 0; i < len(src); {
		c := src[i]
		switch {
		case c == '"':
			j := i + 1
			for j < len(src) && src[j] != '"' {
				if src[j] == '\\' {
					j++
				}
				j++
			}
			end := j + 1
			if end > len(src) {
				end = len(src)
			}
			k := end
			for k < len(src) && (src[k] == ' ' || src[k] == '\n' || src[k] == '\t' || src[k] == '\r') {
				k++
			}
			code := p.String
			if k < len(src) && src[k] == ':' {
				code = p.Key
			}
			paint(code, src[i:end])
			i = end
		case c == '{':
			stack = append(stack, p.Object)
			paint(p.Object, src[i:i+1])
			i++
		case c == '[':
			stack = append(stack, p.Array)
			paint(p.Array, src[i:i+1])
			i++
		case c == '}' || c == ']':
			paint(top(), src[i:i+1])
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
			i++
		case c == ',' || c == ':':
			paint(top(), src[i:i+1])
			i++
		case c == ' ' || c == '\n' || c == '\t' || c == '\r':
			out.WriteByte(c)
			i++
		default:
			j := i
			for j < len(src) && !strings.ContainsRune(",:]} \n\t\r", rune(src[j])) {
				j++
			}
			tok := src[i:j]
			code := p.Number
			switch string(tok) {
			case "null":
				code = p.Null
			case "true":
				code = p.True
			case "false":
				code = p.False
			}
			paint(code, tok)
			i = j
		}
	}
	return out.Bytes()
}

// emit writes finished JSON text, coloured when asked to.
func emit(w interface{ Write([]byte) (int, error) }, b []byte, o Options) error {
	if o.Color {
		b = Colorize(b, PaletteFromEnv())
	}
	_, err := w.Write(b)
	return err
}
