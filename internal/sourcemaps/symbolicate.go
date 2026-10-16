package sourcemaps

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"path"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"monitor/internal/sentry"
	"monitor/internal/store"
)

const (
	// contextLines is how many source lines symbolicated frames carry before
	// and after the error line, as SDKs send them.
	contextLines = 5
	// maxContextLine is the longest source line kept in a frame; longer
	// lines (minified code in node_modules) are cut.
	maxContextLine = 300
	// maxParseBytes is the largest file that is parsed: larger ones could
	// never stay in the cache, and parsing them for every event costs too much.
	maxParseBytes = DefaultCacheBytes
	// nameSearchSegments is how many mappings the search for a function's
	// definition looks back at most.
	nameSearchSegments = 20000
)

// Symbolicate maps the frames of an event's stack traces that come from
// minified files with an uploaded source map back to the original file,
// line, column and function, with the lines of code around them from the
// map's sourcesContent (ruling 15). A map is found by the debug id the SDK
// lists for the file (debug_meta.images of type "sourcemap"), else by the
// event's release and the file's URL. Code under node_modules stops being
// in-app. The minified frames are kept in raw_stacktrace, as Sentry does,
// and an exception that has them is not done again, so calling Symbolicate
// at ingest and again on read is safe. It reports whether anything changed;
// frames without a map stay as they are.
//
// Ingest calls it before scrubbing, while the frames still have their
// original URLs; the issue page calls it for events stored before their
// map was uploaded.
func (s *Service) Symbolicate(ctx context.Context, projectID int64, ev *sentry.Event) (changed bool) {
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("sourcemaps: symbolicate failed", "project", projectID, "event", ev.EventID, "panic", r)
		}
		if changed {
			ev.Refresh() // the typed fields follow Raw, also after a panic in a later exception
		}
	}()
	images := map[string]string{} // minified file URL → debug id
	for _, img := range ev.DebugImages {
		if strings.EqualFold(img.Type, "sourcemap") && img.CodeFile != "" {
			if id := normalizeDebugID(img.DebugID); id != "" {
				images[img.CodeFile] = id
			}
		}
	}
	if len(images) == 0 && ev.Release == "" {
		return false // no way to find a map
	}

	f := &finder{s: s, ctx: ctx, projectID: projectID, release: ev.Release, images: images, found: map[string]*resolved{}}
	for _, exc := range exceptionValues(ev.Raw) {
		if _, done := exc["raw_stacktrace"]; done {
			continue
		}
		stacktrace, _ := exc["stacktrace"].(map[string]any)
		frames, _ := stacktrace["frames"].([]any)
		mapped, ok := f.frames(frames)
		if !ok {
			continue
		}
		exc["raw_stacktrace"] = map[string]any{"frames": frames}
		newTrace := make(map[string]any, len(stacktrace))
		for k, v := range stacktrace {
			newTrace[k] = v
		}
		newTrace["frames"] = mapped
		exc["stacktrace"] = newTrace
		changed = true
	}
	return changed
}

// exceptionValues returns the exception objects of a raw event, whether
// it sends {"values": [...]} or a bare list.
func exceptionValues(raw map[string]any) []map[string]any {
	var list []any
	switch v := raw["exception"].(type) {
	case map[string]any:
		list, _ = v["values"].([]any)
	case []any:
		list = v
	}
	var out []map[string]any
	for _, item := range list {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// resolved is the source map (and minified file) found for one minified file.
type resolved struct {
	m        *sourceMap
	minified []codeLine // nil when the bundle had no minified file
	mapFile  string     // the map's URL in its bundle, such as ~/assets/index.js.map
	how      string     // "debug-id" or "release"
}

// finder looks maps up for the frames of one event, each file once.
type finder struct {
	s         *Service
	ctx       context.Context
	projectID int64
	release   string
	images    map[string]string
	found     map[string]*resolved // by minified file URL; nil when it has none
}

// frameInfo is a frame with what is needed to map it.
type frameInfo struct {
	raw       map[string]any
	file      string
	line, col int // 1-based, as SDKs send them
	r         *resolved
}

// frames returns the frames mapped (new objects; the old ones are not
// changed), and false when not one of them could be mapped.
func (f *finder) frames(frames []any) ([]any, bool) {
	infos := make([]frameInfo, len(frames))
	for i, item := range frames {
		raw, _ := item.(map[string]any)
		infos[i].raw = raw
		if raw == nil {
			continue
		}
		file := text(raw["abs_path"])
		if file == "" {
			file = text(raw["filename"])
		}
		infos[i].file = file
		infos[i].line = number(raw["lineno"])
		infos[i].col = number(raw["colno"])
		if file != "" && infos[i].line > 0 {
			infos[i].r = f.lookup(file)
		}
	}

	out := make([]any, len(frames))
	mappedOne := false
	for i, info := range infos {
		out[i] = frames[i]
		if info.raw == nil {
			continue
		}
		var caller *frameInfo
		if i > 0 {
			caller = &infos[i-1] // the frame before is the call that led here
		}
		if mapped := mapFrame(info, caller); mapped != nil {
			out[i] = mapped
			mappedOne = true
		} else {
			out[i] = copyMap(info.raw) // raw_stacktrace keeps the original object
		}
	}
	return out, mappedOne
}

// lookup returns the source map for a minified file's URL, or nil.
func (f *finder) lookup(file string) *resolved {
	if r, ok := f.found[file]; ok {
		return r
	}
	r, err := f.find(file)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		f.s.log.Warn("sourcemaps: look up a source map", "project", f.projectID, "file", file, "err", err)
	}
	f.found[file] = r
	return r
}

func (f *finder) find(file string) (*resolved, error) {
	db := f.s.db
	if id := f.debugID(file); id != "" {
		row, err := db.SourceMapByDebugID(f.ctx, f.projectID, id, store.SourceMapKind)
		if err == nil {
			min, minErr := db.SourceMapByDebugID(f.ctx, f.projectID, id, store.MinifiedSourceKind)
			return f.s.load(row, min, minErr, "debug-id")
		}
		if !errors.Is(err, store.ErrNotFound) {
			return nil, err
		}
	}
	if f.release == "" || !strings.Contains(file, "://") {
		return nil, store.ErrNotFound
	}
	// By release: the minified file under its URL (or ~/ and its path),
	// then the map it names, or else the same URL with .map.
	candidates := artifactURLs(file)
	min, minErr := db.SourceMapByFile(f.ctx, f.projectID, f.release, store.MinifiedSourceKind, candidates...)
	var mapURLs []string
	if minErr == nil && min.Sourcemap != "" && !strings.HasPrefix(min.Sourcemap, "data:") {
		mapURLs = append(mapURLs, resolveArtifact(min.File, min.Sourcemap))
	}
	for _, c := range candidates {
		mapURLs = append(mapURLs, c+".map")
	}
	row, err := db.SourceMapByFile(f.ctx, f.projectID, f.release, store.SourceMapKind, mapURLs...)
	if err != nil {
		return nil, err
	}
	return f.s.load(row, min, minErr, "release")
}

// debugID returns the debug id the event lists for a file, or "".
func (f *finder) debugID(file string) string {
	if id, ok := f.images[file]; ok {
		return id
	}
	plain := withoutQuery(file)
	for codeFile, id := range f.images {
		if withoutQuery(codeFile) == plain {
			return id
		}
	}
	return ""
}

// artifactURLs are the names a minified file may have in a bundle: its URL
// without query and fragment, and "~" with its path (any host).
func artifactURLs(file string) []string {
	plain := withoutQuery(file)
	out := []string{plain}
	if u, err := url.Parse(plain); err == nil && u.Host != "" {
		out = append(out, "~"+u.EscapedPath())
		if u.EscapedPath() != u.Path {
			out = append(out, "~"+u.Path)
		}
	}
	return out
}

// resolveArtifact resolves ref (a Sourcemap header) against the bundle URL
// of the minified file: "~/assets/index.js" and "index.js.map" give
// "~/assets/index.js.map".
func resolveArtifact(base, ref string) string {
	tilde := strings.HasPrefix(base, "~/")
	if tilde {
		base = "http://tilde.invalid" + base[1:]
	}
	b, err := url.Parse(base)
	r, err2 := url.Parse(ref)
	if err != nil || err2 != nil {
		return ref
	}
	out := b.ResolveReference(r)
	if tilde && out.Host == "tilde.invalid" {
		return "~" + out.EscapedPath()
	}
	return out.String()
}

// load returns a map row parsed, with its minified file when there is one.
func (s *Service) load(row store.SourceMapFile, min store.SourceMapFile, minErr error, how string) (*resolved, error) {
	m, err := s.parsedMap(row)
	if err != nil {
		return nil, err
	}
	r := &resolved{m: m, mapFile: row.File, how: how}
	if minErr == nil {
		if lines, err := s.minifiedLines(min); err == nil {
			r.minified = lines
		} else {
			s.log.Warn("sourcemaps: read a minified file", "file", min.File, "err", err)
		}
	}
	return r, nil
}

// unusable is cached for a file that cannot be parsed, so a broken map is
// not read again for every event.
type unusable struct{ err error }

func (s *Service) parsedMap(row store.SourceMapFile) (*sourceMap, error) {
	v, err := s.cache.get(row.ID, func() (any, int64, error) {
		if row.Size > maxParseBytes {
			return unusable{errors.New("source map too large to use")}, 64, nil
		}
		data, err := s.readFile(row)
		if err != nil {
			return nil, 0, err
		}
		m, err := parseSourceMap(data)
		if err != nil {
			s.log.Warn("sourcemaps: source map cannot be used", "file", row.File, "err", err)
			return unusable{err}, 64, nil
		}
		return m, m.size, nil
	})
	if err != nil {
		return nil, err
	}
	if bad, ok := v.(unusable); ok {
		return nil, bad.err
	}
	return v.(*sourceMap), nil
}

func (s *Service) minifiedLines(row store.SourceMapFile) ([]codeLine, error) {
	v, err := s.cache.get(row.ID, func() (any, int64, error) {
		if row.Size > maxParseBytes {
			return unusable{errors.New("minified file too large to use")}, 64, nil
		}
		data, err := s.readFile(row)
		if err != nil {
			return nil, 0, err
		}
		lines := splitCode(string(data))
		return lines, int64(len(data)) + int64(len(lines))*32, nil
	})
	if err != nil {
		return nil, err
	}
	if bad, ok := v.(unusable); ok {
		return nil, bad.err
	}
	return v.([]codeLine), nil
}

// mapFrame returns the frame mapped to its original file, or nil when it
// has no map or its position maps to no file.
func mapFrame(info frameInfo, caller *frameInfo) map[string]any {
	r := info.r
	if r == nil {
		return nil
	}
	col := max(info.col-1, 0)
	mp, ok := r.m.lookup(info.line-1, col)
	if !ok {
		return nil
	}
	source := resolveSource(info.file, r.m.sources[mp.source])

	out := copyMap(info.raw)
	out["filename"] = source
	out["abs_path"] = source
	// Numbers as the JSON decoder makes them, so the event reads them back.
	out["lineno"] = json.Number(strconv.Itoa(int(mp.line) + 1))
	out["colno"] = json.Number(strconv.Itoa(int(mp.col) + 1))
	if fn := text(info.raw["function"]); fn != "" {
		out["function"] = originalFunction(fn, info, caller)
	}
	if strings.Contains(source, "/node_modules/") {
		out["in_app"] = false
	}
	delete(out, "pre_context")
	delete(out, "context_line")
	delete(out, "post_context")
	if lines := r.m.sourceLines(int(mp.source)); int(mp.line) < len(lines) {
		l := int(mp.line)
		out["pre_context"] = contextOf(lines[max(0, l-contextLines):l])
		out["context_line"] = cutLine(lines[l])
		out["post_context"] = contextOf(lines[l+1 : min(len(lines), l+1+contextLines)])
	}
	data := map[string]any{}
	if old, ok := info.raw["data"].(map[string]any); ok {
		for k, v := range old {
			data[k] = v
		}
	}
	data["sourcemap"] = r.mapFile
	data["resolved_with"] = r.how
	data["symbolicated"] = true
	out["data"] = data
	return out
}

// resolveSource returns the URL of an original file: its name in the map
// resolved against the minified file's URL, as browsers resolve it (the
// map sits next to the minified file). A Vite build's "../../src/main.ts"
// for https://shop.example.com/assets/index.js is
// https://shop.example.com/src/main.ts.
func resolveSource(minified, source string) string {
	if strings.Contains(source, "://") {
		if u, err := url.Parse(source); err == nil {
			return u.ResolveReference(&url.URL{}).String() // drops ./ and ../
		}
		return source
	}
	base, err := url.Parse(withoutQuery(minified))
	if err == nil && base.Scheme != "" {
		return base.ResolveReference(&url.URL{Path: source}).String()
	}
	if strings.HasPrefix(source, "/") {
		return path.Clean(source)
	}
	return path.Join(path.Dir(withoutQuery(minified)), source)
}

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

func contextOf(lines []string) []any {
	out := make([]any, len(lines))
	for i, l := range lines {
		out[i] = cutLine(l)
	}
	return out
}

// cutLine shortens a source line to maxContextLine characters.
func cutLine(s string) string {
	if utf8.RuneCountInString(s) <= maxContextLine {
		return s
	}
	return string([]rune(s)[:maxContextLine-1]) + "…"
}

func withoutQuery(s string) string {
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		return s[:i]
	}
	return s
}

func copyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// text returns a JSON string as it is, anything else as "".
func text(v any) string {
	s, _ := v.(string)
	return s
}

// number reads a JSON number (json.Number or float64) as an int; anything else is 0.
func number(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case interface{ Int64() (int64, error) }:
		i, err := n.Int64()
		if err != nil {
			return 0
		}
		return int(i)
	}
	return 0
}
