package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Required environment variables. The daemon refuses to start without these.
// Their values are never printed; only their names (D36, operator-surface §5 step 1).
const (
	EnvGitHubToken  = "IDLE_DECK_GITHUB_TOKEN"
	EnvRepos        = "IDLE_DECK_REPOS"
	EnvHarnessURL   = "IDLE_DECK_HARNESS_URL"
	EnvHarnessToken = "IDLE_DECK_HARNESS_TOKEN"
)

var requiredVars = []string{EnvGitHubToken, EnvRepos, EnvHarnessURL, EnvHarnessToken}

// secretFields names the Config fields whose values must never appear in logs
// or printed output. Redact replaces them with the literal "<redacted>" (D36).
var secretFields = []string{"GitHubToken", "HarnessToken"}

// defaultSweepPrompt is the shipped default for IDLE_DECK_SWEEP_PROMPT (D43).
// The content is operator-owned; this is the narrow default (operator-surface §4).
const defaultSweepPrompt = "Scan the repository for `TODO` and `FIXME` markers. For each, report the file, line, the marker's age in days, and whether the surrounding file has changed more recently than the marker was added. Report only markers older than 90 days. Produce a report; make no changes."

// defaultDBPath returns the default SQLite path with ~ expanded (operator-surface §4).
func defaultDBPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
	}
	return filepath.Join(home, "Library", "Application Support", "idle-deck", "idle-deck.db")
}

// expandHome expands a leading ~ in a path to the user's home directory.
func expandHome(p string) string {
	if p == "" {
		return p
	}
	if p[0] == '~' {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return p
		}
		if len(p) == 1 {
			return home
		}
		return filepath.Join(home, p[1:])
	}
	return p
}

// fieldKind identifies how a config value is parsed from env/flags.
type fieldKind int

const (
	kindString fieldKind = iota
	kindInt
	kindDurationSeconds
)

// fieldMeta describes one configuration field: its name, env var, kind, default,
// whether it's a secret, and its CLI flag name (empty for secrets).
type fieldMeta struct {
	name     string
	key      string
	kind     fieldKind
	default_ string
	secret   bool
	flag     string
}

// fields is the single source of truth for all configuration fields.
// Required fields have no default; they must be provided via env (D34).
// Defaults match operator-surface §4 exactly.
var fields = []fieldMeta{
	// Required (no defaults; empty = missing).
	{name: "GitHubToken", key: "IDLE_DECK_GITHUB_TOKEN", kind: kindString, secret: true},
	{name: "Repos", key: "IDLE_DECK_REPOS", kind: kindString},
	{name: "HarnessURL", key: "IDLE_DECK_HARNESS_URL", kind: kindString},
	{name: "HarnessToken", key: "IDLE_DECK_HARNESS_TOKEN", kind: kindString, secret: true},

	// Defaults (operator-surface §4).
	{name: "DB", key: "IDLE_DECK_DB", kind: kindString, default_: "~/Library/Application Support/idle-deck/idle-deck.db", flag: "db"},
	{name: "PollInterval", key: "IDLE_DECK_POLL_INTERVAL", kind: kindDurationSeconds, default_: "60", flag: "poll-interval"},
	{name: "MaxConcurrent", key: "IDLE_DECK_MAX_CONCURRENT_JOBS", kind: kindInt, default_: "1", flag: "max-concurrent-jobs"},
	{name: "LogLevel", key: "IDLE_DECK_LOG_LEVEL", kind: kindString, default_: "info", flag: "log-level"},
	{name: "LogFormat", key: "IDLE_DECK_LOG_FORMAT", kind: kindString, default_: "json", flag: "log-format"},
	{name: "TierTimeoutPlan", key: "IDLE_DECK_TIER_TIMEOUT_PLAN", kind: kindDurationSeconds, default_: "900", flag: "tier-timeout-plan"},
	{name: "TierTimeoutDo", key: "IDLE_DECK_TIER_TIMEOUT_DO", kind: kindDurationSeconds, default_: "10800", flag: "tier-timeout-do"},
	{name: "TierTimeoutSweep", key: "IDLE_DECK_TIER_TIMEOUT_SWEEP", kind: kindDurationSeconds, default_: "3600", flag: "tier-timeout-sweep"},
	{name: "TierTimeoutHotfix", key: "IDLE_DECK_TIER_TIMEOUT_HOTFIX", kind: kindDurationSeconds, default_: "1800", flag: "tier-timeout-hotfix"},
	{name: "BudgetPlan", key: "IDLE_DECK_BUDGET_PLAN", kind: kindInt, default_: "5000", flag: "budget-plan"},
	{name: "BudgetDo", key: "IDLE_DECK_BUDGET_DO", kind: kindInt, default_: "100000", flag: "budget-do"},
	{name: "BudgetSweep", key: "IDLE_DECK_BUDGET_SWEEP", kind: kindInt, default_: "20000", flag: "budget-sweep"},
	{name: "BudgetHotfix", key: "IDLE_DECK_BUDGET_HOTFIX", kind: kindInt, default_: "50000", flag: "budget-hotfix"},
	{name: "GitHubAPI", key: "IDLE_DECK_GITHUB_API", kind: kindString, default_: "https://api.github.com", flag: "github-api"},
	{name: "RetryBackoffInitial", key: "IDLE_DECK_RETRY_BACKOFF_INITIAL", kind: kindDurationSeconds, default_: "30", flag: "retry-backoff-initial"},
	{name: "RetryBackoffMax", key: "IDLE_DECK_RETRY_BACKOFF_MAX", kind: kindDurationSeconds, default_: "600", flag: "retry-backoff-max"},
	{name: "SweepPeriod", key: "IDLE_DECK_SWEEP_PERIOD", kind: kindDurationSeconds, default_: "604800", flag: "sweep-period"},
	{name: "SweepRepos", key: "IDLE_DECK_SWEEP_REPOS", kind: kindString, default_: "", flag: "sweep-repos"},
	{name: "SweepPrompt", key: "IDLE_DECK_SWEEP_PROMPT", kind: kindString, default_: defaultSweepPrompt, flag: "sweep-prompt"},
}

// Config holds the resolved idle-deck configuration.
// Required fields have no defaults; the daemon refuses to start if empty.
// All durations are stored as time.Duration (seconds internally).
type Config struct {
	GitHubToken  string
	Repos        string
	HarnessURL   string
	HarnessToken string

	DB                  string
	PollInterval        time.Duration
	MaxConcurrent       int
	LogLevel            string
	LogFormat           string
	TierTimeoutPlan     time.Duration
	TierTimeoutDo       time.Duration
	TierTimeoutSweep    time.Duration
	TierTimeoutHotfix   time.Duration
	BudgetPlan          int
	BudgetDo            int
	BudgetSweep         int
	BudgetHotfix        int
	GitHubAPI           string
	RetryBackoffInitial time.Duration
	RetryBackoffMax     time.Duration
	SweepPeriod         time.Duration
	SweepRepos          string
	SweepPrompt         string
}

// MissingVarError names the required variables that are absent or empty.
// It prints the names only, never the values (D36, operator-surface §5.1).
type MissingVarError struct {
	Vars []string
}

func (e *MissingVarError) Error() string {
	return "missing required configuration variables: " + strings.Join(e.Vars, ", ")
}

// ParseError reports a single environment variable whose value could not be parsed.
// It names the variable and the expected type (e.g. "duration", "int").
type ParseError struct {
	Var   string
	Kind  string
	Cause error
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("%s: invalid %s: %v", e.Var, e.Kind, e.Cause)
}

// setField parses raw according to f.kind and sets the named field on root.
// It is used for defaults, environment, and CLI flags.
func setField(root reflect.Value, f fieldMeta, raw string) error {
	field := root.FieldByName(f.name)
	if !field.IsValid() || !field.CanSet() {
		return fmt.Errorf("config: field %q not settable", f.name)
	}
	switch f.kind {
	case kindString:
		field.SetString(raw)
	case kindInt:
		n, err := strconv.Atoi(raw)
		if err != nil {
			return &ParseError{Var: f.key, Kind: "int", Cause: err}
		}
		field.SetInt(int64(n))
	case kindDurationSeconds:
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return &ParseError{Var: f.key, Kind: "duration", Cause: fmt.Errorf("expected an integer number of seconds, got empty string")}
		}
		n, err := strconv.Atoi(raw)
		if err != nil {
			return &ParseError{Var: f.key, Kind: "duration", Cause: fmt.Errorf("expected an integer number of seconds, got %q", raw)}
		}
		field.SetInt(int64(n) * int64(time.Second))
	default:
		return fmt.Errorf("config: unknown field kind for %q", f.name)
	}
	return nil
}

// FromEnv reads all environment variables, applies defaults, and validates types.
// It does not check for missing required variables (Validate does that).
func FromEnv() (Config, error) {
	c := Defaults()
	for _, f := range fields {
		val := os.Getenv(f.key)
		if val == "" {
			continue
		}
		if err := setField(reflect.ValueOf(&c).Elem(), f, val); err != nil {
			return Config{}, err
		}
	}
	return c, nil
}

// fromEnvNoValidate reads environment variables onto defaults, parsing types.
// It does NOT validate required variables or format.
func fromEnvNoValidate() (Config, error) {
	c := Defaults()
	for _, f := range fields {
		val := os.Getenv(f.key)
		if val == "" {
			continue
		}
		if err := setField(reflect.ValueOf(&c).Elem(), f, val); err != nil {
			return Config{}, err
		}
	}
	return c, nil
}

// Load builds a Config with precedence: defaults → env → flags (via flag.Visit).
// Missing required variables or validation failures are returned as errors.
func Load(fo *FlagOverrides) (Config, error) {
	c, err := fromEnvNoValidate()
	if err != nil {
		return Config{}, err
	}
	if err := ApplyFlags(&c, fo); err != nil {
		return Config{}, err
	}
	if err := Validate(c); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Defaults returns a Config with every defaulted field set to its operator-surface §4 value.
// Required fields are left empty.
func Defaults() Config {
	var c Config
	cv := reflect.ValueOf(&c).Elem()
	for _, f := range fields {
		if f.default_ == "" {
			continue
		}
		if err := setField(cv, f, f.default_); err != nil {
			panic(fmt.Sprintf("invalid default for %s: %v", f.key, err))
		}
	}
	return c
}

// Validate checks that all required variables are present and all values
// are well-formed (log level/format, URLs, repos format, positive durations).
func Validate(cfg Config) error {
	if miss := cfg.MissingRequired(); len(miss) > 0 {
		return &MissingVarError{Vars: miss}
	}

	// Durations must be positive (except SweepPeriod which may be 0 = disabled).
	for _, d := range []struct {
		name string
		v    time.Duration
	}{
		{"IDLE_DECK_POLL_INTERVAL", cfg.PollInterval},
		{"IDLE_DECK_TIER_TIMEOUT_PLAN", cfg.TierTimeoutPlan},
		{"IDLE_DECK_TIER_TIMEOUT_DO", cfg.TierTimeoutDo},
		{"IDLE_DECK_TIER_TIMEOUT_SWEEP", cfg.TierTimeoutSweep},
		{"IDLE_DECK_TIER_TIMEOUT_HOTFIX", cfg.TierTimeoutHotfix},
		{"IDLE_DECK_RETRY_BACKOFF_INITIAL", cfg.RetryBackoffInitial},
		{"IDLE_DECK_RETRY_BACKOFF_MAX", cfg.RetryBackoffMax},
		{"IDLE_DECK_SWEEP_PERIOD", cfg.SweepPeriod},
	} {
		if d.name == "IDLE_DECK_SWEEP_PERIOD" {
			if d.v < 0 {
				return fmt.Errorf("%s must be zero (disabled) or positive, got %s", d.name, d.v)
			}
		} else if d.v <= 0 {
			return fmt.Errorf("%s must be a positive duration, got %s", d.name, d.v)
		}
	}
	if cfg.RetryBackoffMax < cfg.RetryBackoffInitial {
		return fmt.Errorf("%s must not be less than %s", "IDLE_DECK_RETRY_BACKOFF_MAX", "IDLE_DECK_RETRY_BACKOFF_INITIAL")
	}

	if cfg.MaxConcurrent < 1 {
		return fmt.Errorf("%s must be at least 1, got %d", "IDLE_DECK_MAX_CONCURRENT_JOBS", cfg.MaxConcurrent)
	}

	switch cfg.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("%s must be one of debug|info|warn|error, got %q", "IDLE_DECK_LOG_LEVEL", cfg.LogLevel)
	}

	switch cfg.LogFormat {
	case "json", "text":
	default:
		return fmt.Errorf("%s must be one of json|text, got %q", "IDLE_DECK_LOG_FORMAT", cfg.LogFormat)
	}

	for _, u := range []struct{ name, val string }{
		{"IDLE_DECK_GITHUB_API", cfg.GitHubAPI},
		{"IDLE_DECK_HARNESS_URL", cfg.HarnessURL},
	} {
		if u.val == "" {
			continue
		}
		parsed, err := url.Parse(u.val)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return fmt.Errorf("%s: invalid URL %q", u.name, u.val)
		}
	}

	if err := validateRepos(cfg.Repos); err != nil {
		return err
	}
	if err := validateRepos(cfg.SweepRepos); err != nil {
		return fmt.Errorf("%s: %w", "IDLE_DECK_SWEEP_REPOS", err)
	}
	// SweepRepos subset check is deferred to the sweep scheduler (R5).
	// The allowlist gate lives in TrackerSource (I1/D30).

	if cfg.DB == "" {
		return fmt.Errorf("%s must not be empty", "IDLE_DECK_DB")
	}
	return nil
}

// MissingRequired returns the names of required variables that are empty.
func (c Config) MissingRequired() []string {
	var missing []string
	if c.GitHubToken == "" {
		missing = append(missing, EnvGitHubToken)
	}
	if c.Repos == "" {
		missing = append(missing, EnvRepos)
	}
	if c.HarnessURL == "" {
		missing = append(missing, EnvHarnessURL)
	}
	if c.HarnessToken == "" {
		missing = append(missing, EnvHarnessToken)
	}
	return missing
}

// parseRepos parses a comma-separated list of owner/repo strings.
// Empty entries are dropped; each must contain exactly one slash and no spaces.
var repoRE = regexp.MustCompile(`^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$`)

func parseRepos(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var out []string
	seen := map[string]bool{}
	for _, p := range strings.Split(raw, ",") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if strings.ContainsAny(p, " \t") {
			return nil, fmt.Errorf("repository %q must not contain spaces", p)
		}
		if !repoRE.MatchString(p) {
			return nil, fmt.Errorf("repository %q must be owner/repo (one slash, alphanumerics/dots/underscores/hyphens only)", p)
		}
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out, nil
}

// validateRepos validates the format of a comma-separated repos string.
// Empty is valid (means "all allowlisted" for SweepRepos, required for Repos).
func validateRepos(raw string) error {
	_, err := parseRepos(raw)
	return err
}

// Redact returns a copy of c with every secret field replaced by "<redacted>".
// This is the single redaction function (D36), applied once before any logging.
func Redact(c Config) Config {
	c.GitHubToken = "<redacted>"
	c.HarnessToken = "<redacted>"
	return c
}

// Redacted is a convenience method equivalent to Redact(c).
func (c Config) Redacted() Config {
	return Redact(c)
}

// Format returns a line-oriented NAME=value rendering of the resolved config.
// Secrets must already be redacted by the caller (use Redact).
func Format(c Config) string {
	var b strings.Builder
	for _, f := range fields {
		v := reflect.ValueOf(c).FieldByName(f.name)
		var s string
		switch f.kind {
		case kindString:
			s = v.String()
		case kindInt:
			s = strconv.FormatInt(v.Int(), 10)
		case kindDurationSeconds:
			s = strconv.FormatInt(v.Int()/int64(time.Second), 10)
		}
		fmt.Fprintf(&b, "%s=%s\n", f.key, s)
	}
	return b.String()
}

// String returns Format(c).
func (c Config) String() string {
	return Format(c)
}

// ParseRepos parses a comma-separated list of owner/repo strings.
// It is an exported wrapper around the unexported parseRepos, so callers
// outside the config package can reuse the validation logic (D34).
func ParseRepos(raw string) ([]string, error) {
	return parseRepos(raw)
}

// ExpandHome expands a leading ~ in a path to the user's home directory.
// It is an exported wrapper around the unexported expandHome.
func ExpandHome(p string) string {
	return expandHome(p)
}

// CheckDB verifies that the SQLite database path is writable.
// It creates the parent directory if needed and attempts a temp-file probe.
// The schema-current check is deferred to storage slice #10 (no schema exists yet).
func CheckDB(path string) error {
	dir := filepath.Dir(expandHome(path))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("cannot create directory %q: %w", dir, err)
	}
	f, err := os.CreateTemp(dir, ".idle-deck-write-test-*")
	if err != nil {
		return fmt.Errorf("directory %q is not writable: %w", dir, err)
	}
	f.Close()
	os.Remove(f.Name())
	// Schema-current check is deferred to storage slice #10; no schema exists yet.
	fmt.Printf("schema: pending (storage slice #10 – no schema exists yet)\n")
	fmt.Printf("SQLite: path %q writable\n", path)
	return nil
}
