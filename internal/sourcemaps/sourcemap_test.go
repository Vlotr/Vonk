package sourcemaps

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestParseSourceMap_Lookup(t *testing.T) {
	// "AAAA,IAAC" maps columns 0 and 4 of line 0; ";;EACE" maps column 2 of
	// line 2 to source 0, line 1, column 2 with the name "b".
	m, err := parseSourceMap([]byte(`{"version":3,"sources":["a.ts"],"sourceRoot":"src",
		"sourcesContent":["one\r\ntwo\nthree"],"names":["a","b"],"mappings":"AAAAA,IAAC;;EACEC"}`))
	if err != nil {
		t.Fatal(err)
	}
	if m.sources[0] != "src/a.ts" {
		t.Errorf("source = %q, want the root in front", m.sources[0])
	}
	for _, c := range []struct {
		line, col     int
		ok            bool
		srcLine, name string
	}{
		{0, 0, true, "0", "a"},
		{0, 3, true, "0", "a"},
		{0, 9, true, "0", ""},
		{1, 0, false, "", ""},
		{2, 1, false, "", ""},
		{2, 2, true, "1", "b"},
		{9, 0, false, "", ""},
	} {
		mp, ok := m.lookup(c.line, c.col)
		if ok != c.ok || (ok && (fmt.Sprint(mp.line) != c.srcLine || m.name(mp) != c.name)) {
			t.Errorf("lookup(%d, %d) = %+v %v", c.line, c.col, mp, ok)
		}
	}
	if lines := m.sourceLines(0); strings.Join(lines, "|") != "one|two|three" {
		t.Errorf("source lines = %q", lines)
	}
}

func TestParseSourceMap_RefusesBrokenMaps(t *testing.T) {
	for name, data := range map[string]string{
		"not JSON":        `{`,
		"version 2":       `{"version":2,"sources":[],"mappings":""}`,
		"index map":       `{"version":3,"sections":[{"offset":{"line":0,"column":0},"map":{}}]}`,
		"bad character":   `{"version":3,"sources":["a"],"mappings":"A!AA"}`,
		"unknown source":  `{"version":3,"sources":["a"],"mappings":"ACAA"}`,
		"unknown name":    `{"version":3,"sources":["a"],"names":[],"mappings":"AAAAA"}`,
		"cut short":       `{"version":3,"sources":["a"],"mappings":"AAAg"}`,
		"huge number":     `{"version":3,"sources":["a"],"mappings":"gggggggggggggggA"}`,
		"negative column": `{"version":3,"sources":["a"],"mappings":"D"}`,
		"two fields":      `{"version":3,"sources":["a"],"mappings":"AA"}`,
	} {
		if _, err := parseSourceMap([]byte(data)); err == nil {
			t.Errorf("%s: parsed without an error", name)
		}
	}
}

func TestCache_KeepsWhatWasUsedLast(t *testing.T) {
	c := newCache(100)
	loads := map[int64]int{}
	get := func(key, size int64) {
		t.Helper()
		v, err := c.get(key, func() (any, int64, error) {
			loads[key]++
			return fmt.Sprint("value ", key), size, nil
		})
		if err != nil || v != fmt.Sprint("value ", key) {
			t.Fatalf("get(%d) = %v, %v", key, v, err)
		}
	}
	get(1, 40)
	get(2, 40)
	get(1, 40) // 1 was used last
	get(3, 40) // over 100: 2 goes
	get(1, 40)
	get(2, 40)
	if loads[1] != 1 || loads[2] != 2 || loads[3] != 1 {
		t.Errorf("loads = %v, want 1 once, 2 again after it was dropped", loads)
	}
	if c.bytes() > 100 {
		t.Errorf("cache holds %d bytes, more than its 100", c.bytes())
	}
	get(4, 500) // larger than the cache: returned, not kept
	get(4, 500)
	if loads[4] != 2 {
		t.Errorf("a value larger than the cache was kept")
	}
}

func TestCache_LoadsOnceForManyRequests(t *testing.T) {
	c := newCache(1 << 20)
	var (
		mu    sync.Mutex
		loads int
		wg    sync.WaitGroup
	)
	release := make(chan struct{})
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.get(7, func() (any, int64, error) {
				mu.Lock()
				loads++
				mu.Unlock()
				<-release
				return "map", 10, nil
			})
		}()
	}
	close(release)
	wg.Wait()
	if loads != 1 {
		t.Errorf("parsed %d times for 20 requests at once, want 1", loads)
	}
	// A load that panics is an error for everyone waiting, not a hang.
	if _, err := c.get(8, func() (any, int64, error) { panic("broken map") }); err == nil {
		t.Error("a panicking load gave no error")
	}
}

// A map an upload brings may be anything: parsing and looking up never
// panic. go test ./internal/sourcemaps/ -run '^$' -fuzz FuzzParseSourceMap -fuzztime 30s
func FuzzParseSourceMap(f *testing.F) {
	for _, name := range []string{"testdata/build-a/index-BM13fAI6.js.map", "testdata/build-b/index-DqFrKOVy.js.map"} {
		data, err := os.ReadFile(name)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
	f.Add([]byte(`{"version":3,"sources":["a"],"names":["x"],"mappings":"AAAAA;;gBACC,E"}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		m, err := parseSourceMap(data)
		if err != nil {
			return
		}
		for line := -1; line < 4; line++ {
			for _, col := range []int{-1, 0, 5, 1 << 20} {
				if mp, ok := m.lookup(line, col); ok {
					m.sourceLines(int(mp.source))
					m.name(mp)
				}
			}
		}
	})
}
