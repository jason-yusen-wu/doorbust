package env

import (
	"slices"
	"testing"
	"time"
)

// These helpers decide the app's entire runtime configuration, and they fail
// silently by design: a mistyped duration falls back rather than erroring. That
// makes the fallback behaviour worth pinning — a typo in .env produces a
// working app with the wrong settings, not a crash.

func TestGetString(t *testing.T) {
	t.Setenv("DOORBUST_TEST_STRING", "value")

	if got := GetString("DOORBUST_TEST_STRING", "fallback"); got != "value" {
		t.Errorf("got %q, want %q", got, "value")
	}
	if got := GetString("DOORBUST_TEST_MISSING", "fallback"); got != "fallback" {
		t.Errorf("got %q, want the fallback", got)
	}

	// An empty value is treated as unset, which is what makes a commented-out
	// .env line behave the same as a deleted one.
	t.Setenv("DOORBUST_TEST_EMPTY", "")
	if got := GetString("DOORBUST_TEST_EMPTY", "fallback"); got != "fallback" {
		t.Errorf("empty value gave %q, want the fallback", got)
	}
}

func TestGetInt(t *testing.T) {
	t.Setenv("DOORBUST_TEST_INT", "42")
	if got := GetInt("DOORBUST_TEST_INT", 7); got != 42 {
		t.Errorf("got %d, want 42", got)
	}

	if got := GetInt("DOORBUST_TEST_MISSING_INT", 7); got != 7 {
		t.Errorf("got %d, want the fallback 7", got)
	}

	// Silent fallback on a bad value: DB_MAX_CONNS=twenty yields the default
	// pool size rather than a startup failure.
	t.Setenv("DOORBUST_TEST_BAD_INT", "twenty")
	if got := GetInt("DOORBUST_TEST_BAD_INT", 7); got != 7 {
		t.Errorf("unparseable value gave %d, want the fallback 7", got)
	}
}

func TestGetDuration(t *testing.T) {
	t.Setenv("DOORBUST_TEST_DURATION", "90s")
	if got := GetDuration("DOORBUST_TEST_DURATION", time.Minute); got != 90*time.Second {
		t.Errorf("got %v, want 90s", got)
	}

	if got := GetDuration("DOORBUST_TEST_MISSING_DURATION", time.Minute); got != time.Minute {
		t.Errorf("got %v, want the fallback", got)
	}

	t.Setenv("DOORBUST_TEST_BAD_DURATION", "ages")
	if got := GetDuration("DOORBUST_TEST_BAD_DURATION", time.Minute); got != time.Minute {
		t.Errorf("unparseable value gave %v, want the fallback", got)
	}

	// Zero is meaningful, not absent: STRIPE_EVENT_POLL_INTERVAL=0 is how the
	// poller is switched off, so it must survive rather than fall back.
	t.Setenv("DOORBUST_TEST_ZERO_DURATION", "0s")
	if got := GetDuration("DOORBUST_TEST_ZERO_DURATION", time.Minute); got != 0 {
		t.Errorf("got %v, want 0 — a zero duration must not fall back", got)
	}
}

// MustGetString is what makes a missing DSN or Cognito setting fail at startup
// rather than on the first request.
func TestMustGetString(t *testing.T) {
	t.Setenv("DOORBUST_TEST_REQUIRED", "present")

	if got := MustGetString("DOORBUST_TEST_REQUIRED"); got != "present" {
		t.Errorf("got %q, want %q", got, "present")
	}

	t.Run("panics when unset", func(t *testing.T) {
		defer func() {
			if recover() == nil {
				t.Error("expected a panic for a missing required variable")
			}
		}()
		MustGetString("DOORBUST_TEST_DEFINITELY_UNSET")
	})

	t.Run("panics when empty", func(t *testing.T) {
		// An empty value is as unusable as a missing one; treating it as
		// present would start the app with a blank DSN.
		t.Setenv("DOORBUST_TEST_REQUIRED_EMPTY", "")
		defer func() {
			if recover() == nil {
				t.Error("expected a panic for an empty required variable")
			}
		}()
		MustGetString("DOORBUST_TEST_REQUIRED_EMPTY")
	})
}

func TestGetStringSlice(t *testing.T) {
	const key = "DOORBUST_TEST_ORIGINS"

	tests := []struct {
		name  string
		set   bool
		value string
		want  []string
	}{
		// Nil rather than []string{""}: callers treat "no entries" as an off
		// switch, and a list holding one empty string would read as configured.
		{name: "unset is nil", want: nil},
		{name: "empty is nil", set: true, value: "", want: nil},
		{name: "only separators is nil", set: true, value: " , ,", want: nil},
		{name: "single", set: true, value: "http://localhost:5173", want: []string{"http://localhost:5173"}},
		{
			name:  "multiple",
			set:   true,
			value: "http://localhost:5173,https://app.example",
			want:  []string{"http://localhost:5173", "https://app.example"},
		},
		// .env values are unquoted and hand-edited, so stray spaces and a
		// trailing comma are the normal case, not an abuse.
		{
			name:  "trims and drops blanks",
			set:   true,
			value: " http://a.test , https://b.test ,",
			want:  []string{"http://a.test", "https://b.test"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.set {
				t.Setenv(key, tt.value)
			}

			got := GetStringSlice(key)
			if !slices.Equal(got, tt.want) {
				t.Errorf("GetStringSlice(%q) = %#v, want %#v", tt.value, got, tt.want)
			}
		})
	}
}
