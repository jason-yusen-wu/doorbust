package report

import "os"

func osHostname() (string, error) { return os.Hostname() }

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
