package env

import (
	"os"
	"strconv"
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

func MustGetString(key string) string {
	val := os.Getenv(key)
	if val == "" {
		panic(key + " is required — set it in .env or the environment")
	}
	return val
}
