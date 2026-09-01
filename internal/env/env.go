package env

import (
	"os"
	"strconv"
	"strings"
	"time"
)

func GetString(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}

func GetInt(key string, fallback int) int {
	val := os.Getenv(key)
	if val == "" {
		return fallback
	}

	parsed, err := strconv.Atoi(val)
	if err != nil {
		return fallback
	}
	return parsed
}

func GetDuration(key string, fallback time.Duration) time.Duration {
	val := os.Getenv(key)
	if val == "" {
		return fallback
	}

	parsed, err := time.ParseDuration(val)
	if err != nil {
		return fallback
	}
	return parsed
}

// GetStringSlice reads a comma-separated list. Blank entries are dropped and
// each item is trimmed, so a trailing comma or a space after one is harmless —
// worth handling, because this is read from a .env file whose values are
// unquoted and hand-edited.
//
// An unset or all-blank value yields nil, not an empty-string element. Callers
// treat "no entries" as a meaningful off switch, and a list containing "" would
// otherwise read as configured.
func GetStringSlice(key string) []string {
	var out []string
	for _, part := range strings.Split(os.Getenv(key), ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func MustGetString(key string) string {
	val := os.Getenv(key)
	if val == "" {
		panic(key + " is required — set it in .env or the environment")
	}
	return val
}
