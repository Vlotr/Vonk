package posthog

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
)

// The answers to the endpoints posthog-js reads before and while it sends:
// no feature flags, and everything this tool does not collect turned off
// (session recording, surveys, autocapture, heatmaps, web vitals, dead
// clicks, exception autocapture), so the SDK sends page views and events
// only and loads no extra scripts. The shapes are what posthog-js 1.438
// reads (RemoteConfig in @posthog/browser-common).

// FlagsAnswer is the answer to /flags/: no flags.
type FlagsAnswer struct {
	Flags                     map[string]any `json:"flags"`
	ErrorsWhileComputingFlags bool           `json:"errorsWhileComputingFlags"`
	RequestID                 string         `json:"requestId"`
}

// Flags returns the answer to /flags/, with a fresh request id.
func Flags() FlagsAnswer {
	return FlagsAnswer{Flags: map[string]any{}, RequestID: newUUID()}
}

// Config is the remote configuration posthog-js loads from
// /array/<token>/config(.js).
type Config struct {
	Token                 string         `json:"token"`
	SupportedCompression  []string       `json:"supportedCompression"`
	HasFeatureFlags       bool           `json:"hasFeatureFlags"`
	CaptureDeadClicks     bool           `json:"captureDeadClicks"`
	CapturePerformance    bool           `json:"capturePerformance"`
	AutocaptureOptOut     bool           `json:"autocapture_opt_out"`
	AutocaptureExceptions bool           `json:"autocaptureExceptions"`
	Analytics             map[string]any `json:"analytics"`
	ElementsChainAsString bool           `json:"elementsChainAsString"`
	ErrorTracking         map[string]any `json:"errorTracking"`
	Logs                  map[string]any `json:"logs"`
	SessionRecording      bool           `json:"sessionRecording"`
	Heatmaps              bool           `json:"heatmaps"`
	Surveys               bool           `json:"surveys"`
	ProductTours          bool           `json:"productTours"`
	Conversations         bool           `json:"conversations"`
	DefaultIdentifiedOnly bool           `json:"defaultIdentifiedOnly"`
	SiteApps              []any          `json:"siteApps"`
}

// RemoteConfig returns the configuration for a project token. It offers
// gzip-js, so posthog-js compresses its requests, and sends events to
// /i/v0/e/.
func RemoteConfig(token string) Config {
	return Config{
		Token:                 token,
		SupportedCompression:  []string{"gzip", "gzip-js"},
		AutocaptureOptOut:     true,
		Analytics:             map[string]any{"endpoint": "/i/v0/e/"},
		ElementsChainAsString: true,
		ErrorTracking:         map[string]any{"autocaptureExceptions": false, "suppressionRules": []any{}},
		Logs:                  map[string]any{"captureConsoleLogs": false},
		DefaultIdentifiedOnly: true,
		SiteApps:              []any{},
	}
}

// RemoteConfigJS returns /array/<token>/config.js: the script form of the
// configuration, which posthog-js loads with a script tag.
func RemoteConfigJS(token string) string {
	tokenJSON, _ := json.Marshal(token) // escapes quotes and <, > and &
	config, _ := json.Marshal(RemoteConfig(token))
	return fmt.Sprintf("(function() {\n  window._POSTHOG_REMOTE_CONFIG = window._POSTHOG_REMOTE_CONFIG || {};\n"+
		"  window._POSTHOG_REMOTE_CONFIG[%s] = {config: %s, siteApps: []};\n})();\n", tokenJSON, config)
}

// DecideAnswer is the answer to the older /decide/ endpoint: the remote
// configuration and the flags in one, under both old and new names.
type DecideAnswer struct {
	Config
	SiteConfig                map[string]any `json:"config"`
	FeatureFlags              map[string]any `json:"featureFlags"`
	FeatureFlagPayloads       map[string]any `json:"featureFlagPayloads"`
	Flags                     map[string]any `json:"flags"`
	ErrorsWhileComputingFlags bool           `json:"errorsWhileComputingFlags"`
	RequestID                 string         `json:"requestId"`
	ToolbarParams             map[string]any `json:"toolbarParams"`
	IsAuthenticated           bool           `json:"isAuthenticated"`
}

// Decide returns the answer to /decide/.
func Decide() DecideAnswer {
	c := RemoteConfig("")
	c.Token = ""
	return DecideAnswer{
		Config:              c,
		SiteConfig:          map[string]any{"enable_collect_everything": false},
		FeatureFlags:        map[string]any{},
		FeatureFlagPayloads: map[string]any{},
		Flags:               map[string]any{},
		RequestID:           newUUID(),
		ToolbarParams:       map[string]any{},
	}
}

// newUUID returns a random version 4 UUID.
func newUUID() string {
	var b [16]byte
	rand.Read(b[:]) // never fails; see crypto/rand
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
