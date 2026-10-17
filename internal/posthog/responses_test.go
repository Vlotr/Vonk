package posthog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestPosthog_FlagsShape(t *testing.T) {
	data, err := json.Marshal(Flags())
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	id, _ := got["requestId"].(string)
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`).MatchString(id) {
		t.Errorf("requestId = %q, want a UUID", id)
	}
	delete(got, "requestId")
	want := map[string]any{"flags": map[string]any{}, "errorsWhileComputingFlags": false}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("flags = %s, want {\"flags\":{},\"errorsWhileComputingFlags\":false,\"requestId\":\"<uuid>\"}", data)
	}
	if again, _ := json.Marshal(Flags()); strings.Contains(string(again), id) {
		t.Error("two answers share a requestId")
	}
}

// The recorder answered the fixture capture the way the monitor does; if
// the monitor's answers change, the capture must be redone with them.
func TestPosthog_ConfigMatchesTheCapture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "capture", "responses.json"))
	if err != nil {
		t.Fatal(err)
	}
	var answers []struct {
		Suffix string          `json:"suffix"`
		Body   json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal(data, &answers); err != nil {
		t.Fatal(err)
	}
	for _, a := range answers {
		if a.Suffix != "/config" {
			continue
		}
		var want, got any
		json.Unmarshal(a.Body, &want)
		ours, _ := json.Marshal(RemoteConfig(fixtureToken))
		json.Unmarshal(ours, &got)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("RemoteConfig = %s\nthe capture got %s", ours, a.Body)
		}
		return
	}
	t.Fatal("responses.json has no /config answer")
}

func TestPosthog_ConfigKeepsTheSDKQuiet(t *testing.T) {
	c := RemoteConfig("phc_x")
	if c.SessionRecording || c.Surveys || c.Heatmaps || !c.AutocaptureOptOut || c.CapturePerformance || c.HasFeatureFlags {
		t.Errorf("config turns something on: %+v", c)
	}
	js := RemoteConfigJS("phc_x")
	if !strings.Contains(js, `window._POSTHOG_REMOTE_CONFIG["phc_x"]`) || !strings.Contains(js, `"sessionRecording":false`) {
		t.Errorf("config.js = %s", js)
	}
	// A token is inserted as a JSON string, so a crafted one cannot break out of it.
	if js := RemoteConfigJS(`x"];</script>`); !strings.Contains(js, `["x\"];\u003c/script\u003e"]`) {
		t.Errorf("config.js does not escape the token: %s", js)
	}
	d, _ := json.Marshal(Decide())
	for _, key := range []string{`"featureFlags":{}`, `"sessionRecording":false`, `"supportedCompression":["gzip","gzip-js"]`} {
		if !strings.Contains(string(d), key) {
			t.Errorf("decide lacks %s: %s", key, d)
		}
	}
}
