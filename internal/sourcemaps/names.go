package sourcemaps

import (
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Function names: a minified frame names its function as the minifier
// renamed it ("e", "Object.t"). The map has the original names, but at
// the positions of names in the minified code, not at the frame's
// position, so the original name is found where the function is defined
// (searching back from the frame) or, failing that, at the call in the
// frame before. The minified code is kept as codeLines, which count
// columns in UTF-16 units as source maps do.

// nameSearchSegments is how many mappings the search for a function's
// definition looks back at most.
const nameSearchSegments = 20000

// originalFunction returns the frame's function with its minified name
// replaced by the original one ("Object.e" becomes "Object.showPrice"),
// or fn as it is when the map does not tell.
func originalFunction(fn string, info frameInfo, caller *frameInfo) string {
	id := minifiedName(fn)
	if id == "" || info.r.minified == nil {
		return fn
	}
	name := definitionName(info.r, id, info.line-1, max(info.col-1, 0))
	if name == "" && caller != nil && caller.r == info.r && caller.line > 0 {
		name = callName(info.r, id, caller.line-1, max(caller.col-1, 0))
	}
	if name == "" || name == id {
		return fn
	}
	return fn[:len(fn)-len(id)] + name
}

// minifiedName returns the last name of a frame's function ("e" in
// "Object.e" or "async e"), or "" when it has none ("<anonymous>").
func minifiedName(fn string) string {
	if i := strings.LastIndexAny(fn, ". "); i >= 0 {
		fn = fn[i+1:]
	}
	if fn == "" || (fn[0] >= '0' && fn[0] <= '9') {
		return ""
	}
	for i := 0; i < len(fn); i++ {
		if !isIdentByte(fn[i]) {
			return ""
		}
	}
	return fn
}

// definitionName looks back from a position in the minified code for the
// definition of the function named id (function id(, id=function, id=(...)=>,
// class id) and returns the name the map gives it: the function the
// position is in.
func definitionName(r *resolved, id string, line, col int) string {
	seen := 0
	for l := min(line, len(r.m.lines)-1); l >= 0 && seen < nameSearchSegments; l-- {
		if l >= len(r.minified) {
			continue
		}
		segments := r.m.lines[l]
		for i := len(segments) - 1; i >= 0 && seen < nameSearchSegments; i-- {
			seg := segments[i]
			if l == line && int(seg.genCol) > col {
				continue
			}
			seen++
			if seg.name < 0 {
				continue
			}
			code := r.minified[l]
			if code.at(int(seg.genCol), id) && isDefinition(code, int(seg.genCol), len(id)) {
				return r.m.name(seg)
			}
		}
	}
	return ""
}

// callName returns the name the map gives the call at the caller's
// position, when the minified code there calls id.
func callName(r *resolved, id string, line, col int) string {
	if line >= len(r.minified) {
		return ""
	}
	mp, ok := r.m.lookup(line, col)
	if !ok || int(mp.genCol) != col || !r.minified[line].at(col, id) {
		return ""
	}
	return r.m.name(mp)
}

// isDefinition reports whether the name of n units at col is where a
// function or class gets its name: after "function" or "class", or before
// "=" and a function.
func isDefinition(code codeLine, col, n int) bool {
	before := strings.TrimRight(code.before(col, 16), " \t*")
	for _, kw := range []string{"function", "class"} {
		if strings.HasSuffix(before, kw) {
			rest := before[:len(before)-len(kw)]
			if rest == "" || !isIdentByte(rest[len(rest)-1]) {
				return true
			}
		}
	}
	after := strings.TrimLeft(code.after(col+n, 48), " \t")
	if !strings.HasPrefix(after, "=") || strings.HasPrefix(after, "==") || strings.HasPrefix(after, "=>") {
		return false
	}
	value := strings.TrimLeft(after[1:], " \t")
	for _, start := range []string{"function", "async", "class", "("} {
		if strings.HasPrefix(value, start) {
			return true
		}
	}
	// A single parameter arrow function: e=>...
	i := 0
	for i < len(value) && isIdentByte(value[i]) {
		i++
	}
	return i > 0 && strings.HasPrefix(strings.TrimLeft(value[i:], " \t"), "=>")
}

func isIdentByte(c byte) bool {
	return c == '_' || c == '$' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// codeLine is one line of minified code. Source map columns count UTF-16
// units, so a line with other than ASCII is kept as UTF-16.
type codeLine struct {
	ascii string
	wide  []uint16 // set instead of ascii for lines with other characters
}

func splitCode(s string) []codeLine {
	var out []codeLine
	for line := range strings.SplitSeq(s, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if isASCII(line) {
			out = append(out, codeLine{ascii: line})
		} else {
			out = append(out, codeLine{wide: utf16.Encode([]rune(line))})
		}
	}
	return out
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

func (c codeLine) len() int {
	if c.wide != nil {
		return len(c.wide)
	}
	return len(c.ascii)
}

// slice returns the units from start to end as text; units that are not
// ASCII become "?", which no name or keyword contains.
func (c codeLine) slice(start, end int) string {
	start, end = max(start, 0), min(end, c.len())
	if start >= end {
		return ""
	}
	if c.wide == nil {
		return c.ascii[start:end]
	}
	b := make([]byte, 0, end-start)
	for _, u := range c.wide[start:end] {
		if u < utf8.RuneSelf {
			b = append(b, byte(u))
		} else {
			b = append(b, '?')
		}
	}
	return string(b)
}

// at reports whether the name id stands at col as a whole word.
func (c codeLine) at(col int, id string) bool {
	if c.slice(col, col+len(id)) != id {
		return false
	}
	prev, next := c.slice(col-1, col), c.slice(col+len(id), col+len(id)+1)
	return (prev == "" || !isIdentByte(prev[0])) && (next == "" || !isIdentByte(next[0]))
}

func (c codeLine) before(col, n int) string { return c.slice(col-n, col) }
func (c codeLine) after(col, n int) string  { return c.slice(col, col+n) }
