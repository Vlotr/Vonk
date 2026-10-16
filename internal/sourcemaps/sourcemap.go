package sourcemaps

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// sourceMap is a parsed source map (version 3, as bundlers write it): for
// each line of the minified file, which original file, line, column and
// name each piece of code came from.
type sourceMap struct {
	sources []string // as the map names them, with sourceRoot in front
	names   []string
	// lines holds the mappings of each generated line, sorted by column.
	lines [][]mapping

	content    []string // sourcesContent; "" when missing
	hasContent []bool

	mu          sync.Mutex
	splitSource map[int][]string // content split into lines, made on first use

	size int64 // roughly how much memory the map takes, for the cache
}

// mapping ties a column of a generated line to a place in an original file.
// Lines and columns are 0-based, as in the source map format.
type mapping struct {
	genCol int32
	source int32 // index in sources; -1 for code that came from no file
	line   int32
	col    int32
	name   int32 // index in names; -1 without a name
}

// mappingBytes is roughly what one mapping costs in memory.
const mappingBytes = 24

// parseSourceMap reads a source map. Index maps (with "sections") are not
// supported; bundlers for the browser write plain maps.
func parseSourceMap(data []byte) (*sourceMap, error) {
	var raw struct {
		Version        int               `json:"version"`
		Sources        []*string         `json:"sources"`
		SourceRoot     string            `json:"sourceRoot"`
		SourcesContent []*string         `json:"sourcesContent"`
		Names          []string          `json:"names"`
		Mappings       string            `json:"mappings"`
		Sections       []json.RawMessage `json:"sections"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("source map is not JSON: %w", err)
	}
	if len(raw.Sections) > 0 {
		return nil, errors.New("index source maps (with sections) are not supported")
	}
	if raw.Version != 3 {
		return nil, fmt.Errorf("source map version %d is not supported", raw.Version)
	}

	m := &sourceMap{names: raw.Names, splitSource: map[int][]string{}}
	root := raw.SourceRoot
	if root != "" && !strings.HasSuffix(root, "/") {
		root += "/"
	}
	for i, src := range raw.Sources {
		name := ""
		if src != nil {
			name = *src
		}
		if root != "" && !strings.Contains(name, "://") && !strings.HasPrefix(name, "/") {
			name = root + name
		}
		m.sources = append(m.sources, name)
		content := ""
		has := i < len(raw.SourcesContent) && raw.SourcesContent[i] != nil
		if has {
			content = *raw.SourcesContent[i]
		}
		m.content = append(m.content, content)
		m.hasContent = append(m.hasContent, has)
		m.size += int64(len(content) + len(name))
	}

	lines, err := decodeMappings(raw.Mappings, len(m.sources), len(m.names))
	if err != nil {
		return nil, err
	}
	m.lines = lines
	for _, line := range lines {
		m.size += int64(len(line)) * mappingBytes
	}
	for _, n := range m.names {
		m.size += int64(len(n))
	}
	return m, nil
}

// decodeMappings reads the "mappings" field: lines separated by ";",
// segments by ",", each segment base64 VLQ numbers relative to the
// segment before (the generated column starts over on every line).
func decodeMappings(s string, sources, names int) ([][]mapping, error) {
	var src, srcLine, srcCol, name int
	text := strings.Split(s, ";")
	lines := make([][]mapping, len(text))
	for i, lineText := range text {
		var line []mapping
		genCol := 0
		for segment := range strings.SplitSeq(lineText, ",") {
			if segment == "" {
				continue
			}
			fields, err := decodeVLQ(segment)
			if err != nil {
				return nil, err
			}
			if len(fields) != 1 && len(fields) != 4 && len(fields) != 5 {
				return nil, fmt.Errorf("source map segment has %d fields", len(fields))
			}
			genCol += fields[0]
			if genCol < 0 {
				return nil, errors.New("source map segment has a negative column")
			}
			mp := mapping{genCol: int32(genCol), source: -1, name: -1}
			if len(fields) >= 4 {
				src += fields[1]
				srcLine += fields[2]
				srcCol += fields[3]
				if src < 0 || src >= sources || srcLine < 0 || srcCol < 0 {
					return nil, errors.New("source map segment points outside its sources")
				}
				mp.source, mp.line, mp.col = int32(src), int32(srcLine), int32(srcCol)
			}
			if len(fields) == 5 {
				name += fields[4]
				if name < 0 || name >= names {
					return nil, errors.New("source map segment names a name it does not have")
				}
				mp.name = int32(name)
			}
			line = append(line, mp)
		}
		if !sort.SliceIsSorted(line, func(a, b int) bool { return line[a].genCol < line[b].genCol }) {
			sort.SliceStable(line, func(a, b int) bool { return line[a].genCol < line[b].genCol })
		}
		lines[i] = line
	}
	return lines, nil
}

// decodeVLQ reads the base64 VLQ numbers of one segment.
func decodeVLQ(s string) ([]int, error) {
	var (
		out          []int
		value, shift int
	)
	for i := 0; i < len(s); i++ {
		digit := base64Value(s[i])
		if digit < 0 {
			return nil, fmt.Errorf("source map mappings contain %q", s[i])
		}
		if shift > 25 {
			return nil, errors.New("source map number is too large")
		}
		value += (digit & 31) << shift
		if digit&32 != 0 {
			shift += 5
			continue
		}
		if value&1 != 0 {
			out = append(out, -(value >> 1))
		} else {
			out = append(out, value>>1)
		}
		value, shift = 0, 0
	}
	if shift != 0 {
		return nil, errors.New("source map mappings end in the middle of a number")
	}
	return out, nil
}

func base64Value(c byte) int {
	switch {
	case c >= 'A' && c <= 'Z':
		return int(c - 'A')
	case c >= 'a' && c <= 'z':
		return int(c-'a') + 26
	case c >= '0' && c <= '9':
		return int(c-'0') + 52
	case c == '+':
		return 62
	case c == '/':
		return 63
	}
	return -1
}

// lookup returns the mapping that covers a 0-based line and column of the
// minified file: the last one on that line that starts at or before the
// column. ok is false when the code there came from no original file.
func (m *sourceMap) lookup(line, col int) (mp mapping, ok bool) {
	if line < 0 || line >= len(m.lines) {
		return mapping{}, false
	}
	segments := m.lines[line]
	i := sort.Search(len(segments), func(i int) bool { return int(segments[i].genCol) > col }) - 1
	if i < 0 || segments[i].source < 0 {
		return mapping{}, false
	}
	return segments[i], true
}

// sourceLines returns the lines of an original file from sourcesContent,
// or nil when the map does not carry its content.
func (m *sourceMap) sourceLines(source int) []string {
	if source < 0 || source >= len(m.content) || !m.hasContent[source] {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	lines, ok := m.splitSource[source]
	if !ok {
		lines = strings.Split(m.content[source], "\n")
		for i, l := range lines {
			lines[i] = strings.TrimSuffix(l, "\r")
		}
		m.splitSource[source] = lines
	}
	return lines
}

// name returns the original name of a mapping, or "".
func (m *sourceMap) name(mp mapping) string {
	if mp.name < 0 || int(mp.name) >= len(m.names) {
		return ""
	}
	return m.names[mp.name]
}
