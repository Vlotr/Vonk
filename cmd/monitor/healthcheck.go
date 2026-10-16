package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"time"

	"monitor/internal/config"
)

// healthcheck calls /health on the local server and returns 0 when it answers
// 200, else 1. Docker's HEALTHCHECK runs it, because the runtime image has no
// shell or curl. It needs only ADDR, not the full configuration.
func healthcheck(getenv func(string) string, stderr io.Writer) int {
	addr := getenv("ADDR")
	if addr == "" {
		addr = config.DefaultAddr
	}
	target, err := healthURL(addr)
	if err != nil {
		fmt.Fprintf(stderr, "healthcheck: %v\n", err)
		return 1
	}

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(target)
	if err != nil {
		fmt.Fprintf(stderr, "healthcheck: %v\n", err)
		return 1
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		fmt.Fprintf(stderr, "healthcheck: %s answered %d: %s\n", target, resp.StatusCode, body)
		return 1
	}
	return 0
}

// healthURL turns a listen address such as ":8000" into the URL to call.
// A wildcard host (empty, 0.0.0.0 or ::) is reached through 127.0.0.1.
func healthURL(addr string) (string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("ADDR %q: %w", addr, err)
	}
	if host == "" {
		host = "127.0.0.1"
	} else if ip, err := netip.ParseAddr(host); err == nil && ip.IsUnspecified() {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/health", nil
}
