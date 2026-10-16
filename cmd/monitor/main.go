// Command monitor is a self-hosted error and page-view monitor. It accepts
// traffic from the Sentry and PostHog SDKs and serves a dashboard.
//
// Usage:
//
//	monitor [serve]                     run the server (the default)
//	monitor healthcheck                 exit 0 when the local server is healthy
//	monitor project create --name NAME  create a project and print its keys
package main

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `Usage:
  monitor [serve]                     run the server (the default)
  monitor healthcheck                 exit 0 when the local server is healthy, 1 otherwise
  monitor project create --name NAME  create a project and print its DSN and PostHog token

Settings come from environment variables: SECRET_KEY and BASE_URL are required;
DATABASE_URL, MAIL_URL, ADDR, DATA_DIR, LOG_LEVEL and DEV_BASIC_AUTH are optional.
`

func main() {
	os.Exit(run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr))
}

// run executes one command and returns the process exit code. It takes its
// inputs as arguments so tests can call it directly.
func run(args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	command := "serve"
	if len(args) > 0 {
		command, args = args[0], args[1:]
	}

	switch command {
	case "serve":
		return serve(args, getenv, stderr)
	case "healthcheck":
		return healthcheck(getenv, stderr)
	case "project":
		return projectCommand(args, getenv, stdout, stderr)
	case "help", "-h", "-help", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "monitor: unknown command %q\n\n%s", command, usage)
		return 2
	}
}

// printConfigError shows every configuration problem on its own line.
func printConfigError(stderr io.Writer, err error) {
	fmt.Fprintln(stderr, "monitor: the configuration has problems:")
	for line := range strings.SplitSeq(err.Error(), "\n") {
		fmt.Fprintf(stderr, "  - %s\n", line)
	}
}
