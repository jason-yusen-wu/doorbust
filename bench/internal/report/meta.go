package report

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Meta is everything needed to reproduce or discount a run.
//
// It is recorded by the tool rather than written down by a person on purpose:
// bench sets the app's environment, so it can report the environment without
// anyone having to keep a note that stays accurate.
type Meta struct {
	GitSHA   string `json:"git_sha"`
	GitDirty bool   `json:"git_dirty"`
	// SessionID groups the runs of one sweep. Only runs from the same session
	// are comparable — a comparison across sessions may be measuring what else
	// the laptop was doing.
	SessionID string `json:"session_id"`

	Hostname  string `json:"hostname"`
	GoVersion string `json:"go_version"`
	OSArch    string `json:"os_arch"`
	NumCPU    int    `json:"num_cpu"`

	AppGOMAXPROCS   int    `json:"app_gomaxprocs"`
	BenchGOMAXPROCS int    `json:"bench_gomaxprocs"`
	GOGC            string `json:"gogc"`
	GOMEMLIMIT      string `json:"gomemlimit"`
	RLimitNofile    uint64 `json:"rlimit_nofile"`

	StartedAt time.Time `json:"started_at"`
}

// DBInfo is read back from the server with SHOW rather than assumed from the
// flags bench passed, so a run against a database somebody started by hand with
// different settings identifies itself instead of quietly producing a number
// that cannot be compared.
type DBInfo struct {
	ServerVersion     string `json:"server_version"`
	SynchronousCommit string `json:"synchronous_commit"`
	Fsync             string `json:"fsync"`
	MaxConnections    string `json:"max_connections"`
	SharedBuffers     string `json:"shared_buffers"`
}

type AppInfo struct {
	Arm                     string `json:"arm"`
	CustomerCache           bool   `json:"customer_cache"`
	CustomerCacheEntries    int    `json:"customer_cache_entries,omitempty"`
	LogRequests             bool   `json:"log_requests"`
	DBMaxConns              int    `json:"db_max_conns"`
	ReservationTTL          string `json:"reservation_ttl"`
	SweepInterval           string `json:"sweep_interval"`
	StripeEventPollInterval string `json:"stripe_event_poll_interval"`
}

// Collect gathers everything knowable about this machine and process.
func Collect(sessionID string, appGOMAXPROCS int) Meta {
	m := Meta{
		SessionID:       sessionID,
		GoVersion:       runtime.Version(),
		OSArch:          runtime.GOOS + "/" + runtime.GOARCH,
		NumCPU:          runtime.NumCPU(),
		AppGOMAXPROCS:   appGOMAXPROCS,
		BenchGOMAXPROCS: runtime.GOMAXPROCS(0),
		GOGC:            envOr("GOGC", "100"),
		GOMEMLIMIT:      envOr("GOMEMLIMIT", "off"),
		StartedAt:       time.Now().UTC(),
	}
	m.Hostname, _ = osHostname()
	m.GitSHA, m.GitDirty = gitState()

	var lim syscall.Rlimit
	if err := syscall.Getrlimit(syscall.RLIMIT_NOFILE, &lim); err == nil {
		m.RLimitNofile = uint64(lim.Cur)
	}
	return m
}

// gitState reports the commit a result belongs to. A result that cannot be
// attributed to a commit is worse than no result, so the caller refuses to run
// when the SHA is empty or the tree is dirty unless explicitly allowed.
func gitState() (sha string, dirty bool) {
	out, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil {
		return "", true
	}
	sha = strings.TrimSpace(string(out))

	// Results are excluded from the dirtiness check. The check exists to
	// guarantee the recorded SHA describes the code that produced the numbers,
	// and a result file is an output of that code, not an input to it — a run
	// cannot invalidate itself by writing down what it found. Without this the
	// first recorded run of a session makes the tree dirty and every run after
	// it refuses to start.
	status, err := exec.Command("git", "status", "--porcelain", "--", ".", ":(exclude)bench/results").Output()
	if err != nil {
		return sha, true
	}
	return sha, strings.TrimSpace(string(status)) != ""
}

// ReadDBInfo asks the server what it is actually configured to do.
func ReadDBInfo(ctx context.Context, pool *pgxpool.Pool) DBInfo {
	var d DBInfo
	for _, q := range []struct {
		setting string
		dst     *string
	}{
		{"server_version", &d.ServerVersion},
		{"synchronous_commit", &d.SynchronousCommit},
		{"fsync", &d.Fsync},
		{"max_connections", &d.MaxConnections},
		{"shared_buffers", &d.SharedBuffers},
	} {
		// SHOW does not take a parameter, and the setting names here are
		// constants in this file rather than anything a caller supplies.
		_ = pool.QueryRow(ctx, "SHOW "+q.setting).Scan(q.dst)
	}
	return d
}
