package config

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLoad_MissingRequired(t *testing.T) {
	// Clear all idle-deck env vars to test missing required.
	for _, k := range allEnvVars() {
		os.Unsetenv(k)
	}
	_, err := Load(nil)
	var miss *MissingVarError
	if !errors.As(err, &miss) {
		t.Fatalf("expected *MissingVarError, got %T: %v", err, err)
	}
	if len(miss.Vars) != 4 {
		t.Fatalf("expected 4 missing vars, got %d: %v", len(miss.Vars), miss.Vars)
	}
	for _, want := range requiredVars {
		found := false
		for _, got := range miss.Vars {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing var list missing %q", want)
		}
	}
	// Names only, no values (D36).
	if strings.Contains(miss.Error(), "ghp_") || strings.Contains(miss.Error(), "secret") {
		t.Errorf("error leaks a value: %s", miss.Error())
	}
}

func TestLoad_Defaults(t *testing.T) {
	// Clear env, then set only required vars.
	clearEnv(t)
	os.Setenv(EnvGitHubToken, "ghp_test")
	os.Setenv(EnvRepos, "acme/widgets")
	os.Setenv(EnvHarnessURL, "https://harness.example.com")
	os.Setenv(EnvHarnessToken, "htok")

	cfg, err := Load(nil)
	if err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Secrets redacted.
	red := Redact(cfg)
	if red.GitHubToken != "<redacted>" {
		t.Errorf("GitHubToken not redacted: %q", red.GitHubToken)
	}
	if red.HarnessToken != "<redacted>" {
		t.Errorf("HarnessToken not redacted: %q", red.HarnessToken)
	}
	// Original unchanged (Redact returns a copy).
	if cfg.GitHubToken != "ghp_test" {
		t.Error("Redact mutated original")
	}

	// Non-secret defaults visible.
	if cfg.PollInterval != 60*time.Second {
		t.Errorf("PollInterval default = %v, want 60s", cfg.PollInterval)
	}
	if cfg.MaxConcurrent != 1 {
		t.Errorf("MaxConcurrent default = %d, want 1", cfg.MaxConcurrent)
	}
	if cfg.LogLevel != "info" {
		t.Errorf("LogLevel default = %q, want info", cfg.LogLevel)
	}
	if cfg.LogFormat != "json" {
		t.Errorf("LogFormat default = %q, want json", cfg.LogFormat)
	}
	if cfg.TierTimeoutPlan != 900*time.Second {
		t.Errorf("TierTimeoutPlan default = %v, want 900s", cfg.TierTimeoutPlan)
	}
	if cfg.TierTimeoutDo != 10800*time.Second {
		t.Errorf("TierTimeoutDo default = %v, want 10800s", cfg.TierTimeoutDo)
	}
	if cfg.TierTimeoutSweep != 3600*time.Second {
		t.Errorf("TierTimeoutSweep default = %v, want 3600s", cfg.TierTimeoutSweep)
	}
	if cfg.TierTimeoutHotfix != 1800*time.Second {
		t.Errorf("TierTimeoutHotfix default = %v, want 1800s", cfg.TierTimeoutHotfix)
	}
	if cfg.BudgetPlan != 5000 || cfg.BudgetDo != 100000 || cfg.BudgetSweep != 20000 || cfg.BudgetHotfix != 50000 {
		t.Errorf("budget defaults wrong: plan=%d do=%d sweep=%d hotfix=%d", cfg.BudgetPlan, cfg.BudgetDo, cfg.BudgetSweep, cfg.BudgetHotfix)
	}
	if cfg.GitHubAPI != "https://api.github.com" {
		t.Errorf("GitHubAPI default = %q", cfg.GitHubAPI)
	}
	if cfg.RetryBackoffInitial != 30*time.Second {
		t.Errorf("RetryBackoffInitial default = %v, want 30s", cfg.RetryBackoffInitial)
	}
	if cfg.RetryBackoffMax != 10*time.Minute {
		t.Errorf("RetryBackoffMax default = %v, want 10m", cfg.RetryBackoffMax)
	}
	if cfg.SweepPeriod != 168*time.Hour {
		t.Errorf("SweepPeriod default = %v, want 168h", cfg.SweepPeriod)
	}
	if cfg.DB == "" {
		t.Error("DB default empty")
	}
	if cfg.Repos != "acme/widgets" {
		t.Errorf("Repos = %q, want acme/widgets", cfg.Repos)
	}
	if cfg.DashboardActions {
		t.Error("DashboardActions default = true, want false")
	}
	// D49: the capacity gate defaults off, preserving D31's reachable-only
	// behaviour for every existing deployment.
	if cfg.YieldWhenBusy {
		t.Error("YieldWhenBusy default = true, want false (D49)")
	}
}

func TestDurationSecondsParsing(t *testing.T) {
	// D28/D43: the documented operator contract uses Go duration strings
	// ("60s", "168h"), but existing callers and tests pass bare integers
	// ("60", "900", "604800"). The parser must accept both forms unchanged.
	tests := []struct {
		raw  string
		want time.Duration
	}{
		{"60", 60 * time.Second},
		{"900", 900 * time.Second},
		{"604800", 604800 * time.Second},
		{"60s", 60 * time.Second},
		{"168h", 168 * time.Hour},
		{"900s", 900 * time.Second},
		{"1h30m", 90 * time.Minute},
		{"5m", 5 * time.Minute},
		{"0", 0},
	}
	for _, tc := range tests {
		var c struct {
			D time.Duration
		}
		err := setField(reflect.ValueOf(&c).Elem(), fieldMeta{
			name: "D", key: "IDLE_DECK_TEST_DURATION", kind: kindDurationSeconds,
		}, tc.raw)
		if err != nil {
			t.Errorf("setField(%q) returned error: %v", tc.raw, err)
			continue
		}
		if c.D != tc.want {
			t.Errorf("setField(%q) = %v, want %v", tc.raw, c.D, tc.want)
		}
	}
}

func TestDurationSecondsEmpty(t *testing.T) {
	var c struct {
		D time.Duration
	}
	err := setField(reflect.ValueOf(&c).Elem(), fieldMeta{
		name: "D", key: "IDLE_DECK_TEST_DURATION", kind: kindDurationSeconds,
	}, "")
	if err == nil {
		t.Fatal("expected error for empty duration")
	}
	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("expected *ParseError, got %T: %v", err, err)
	}
	if pe.Var != "IDLE_DECK_TEST_DURATION" || pe.Kind != "duration" {
		t.Errorf("ParseError fields wrong: %+v", pe)
	}
}

func TestDurationSecondsInvalid(t *testing.T) {
	// Must still return a *ParseError for unparseable input.
	for _, raw := range []string{"bad", "12x", "abc def", "1.5"} {
		var c struct {
			D time.Duration
		}
		err := setField(reflect.ValueOf(&c).Elem(), fieldMeta{
			name: "D", key: "IDLE_DECK_TEST_DURATION", kind: kindDurationSeconds,
		}, raw)
		if err == nil {
			t.Errorf("setField(%q) expected error, got nil", raw)
			continue
		}
		var pe *ParseError
		if !errors.As(err, &pe) {
			t.Errorf("setField(%q) expected *ParseError, got %T: %v", raw, err, err)
		}
	}
}

func TestFromEnvChartDocumentedValues(t *testing.T) {
	// The Helm chart (charts/idle-deck/values.yaml) emits Go duration strings
	// for the documented operator contract (operator-surface §4): 60s and 168h.
	// FromEnv must succeed with these set, not crash the daemon on startup.
	clearEnv(t)
	os.Setenv(EnvGitHubToken, "ghp_test")
	os.Setenv(EnvRepos, "acme/widgets")
	os.Setenv(EnvHarnessURL, "https://harness.example.com")
	os.Setenv(EnvHarnessToken, "htok")
	os.Setenv("IDLE_DECK_POLL_INTERVAL", "60s")
	os.Setenv("IDLE_DECK_TIER_TIMEOUT_PLAN", "900s")
	os.Setenv("IDLE_DECK_TIER_TIMEOUT_DO", "10800s")
	os.Setenv("IDLE_DECK_TIER_TIMEOUT_SWEEP", "3600s")
	os.Setenv("IDLE_DECK_TIER_TIMEOUT_HOTFIX", "1800s")
	os.Setenv("IDLE_DECK_RETRY_BACKOFF_INITIAL", "30s")
	os.Setenv("IDLE_DECK_RETRY_BACKOFF_MAX", "10m")
	os.Setenv("IDLE_DECK_SWEEP_PERIOD", "168h")

	cfg, err := FromEnv()
	if err != nil {
		t.Fatalf("FromEnv failed with chart-documented values: %v", err)
	}
	if cfg.PollInterval != 60*time.Second {
		t.Errorf("PollInterval = %v, want 60s", cfg.PollInterval)
	}
	if cfg.TierTimeoutPlan != 900*time.Second {
		t.Errorf("TierTimeoutPlan = %v, want 900s", cfg.TierTimeoutPlan)
	}
	if cfg.TierTimeoutDo != 10800*time.Second {
		t.Errorf("TierTimeoutDo = %v, want 10800s", cfg.TierTimeoutDo)
	}
	if cfg.TierTimeoutSweep != 3600*time.Second {
		t.Errorf("TierTimeoutSweep = %v, want 3600s", cfg.TierTimeoutSweep)
	}
	if cfg.TierTimeoutHotfix != 1800*time.Second {
		t.Errorf("TierTimeoutHotfix = %v, want 1800s", cfg.TierTimeoutHotfix)
	}
	if cfg.RetryBackoffInitial != 30*time.Second {
		t.Errorf("RetryBackoffInitial = %v, want 30s", cfg.RetryBackoffInitial)
	}
	if cfg.RetryBackoffMax != 10*time.Minute {
		t.Errorf("RetryBackoffMax = %v, want 10m", cfg.RetryBackoffMax)
	}
	if cfg.SweepPeriod != 168*time.Hour {
		t.Errorf("SweepPeriod = %v, want 168h", cfg.SweepPeriod)
	}
}

func TestPollIntervalBad(t *testing.T) {
	clearEnv(t)
	os.Setenv(EnvGitHubToken, "ghp_test")
	os.Setenv(EnvRepos, "acme/widgets")
	os.Setenv(EnvHarnessURL, "https://h.example.com")
	os.Setenv(EnvHarnessToken, "htok")
	os.Setenv("IDLE_DECK_POLL_INTERVAL", "bad")

	_, err := Load(nil)
	if err == nil {
		t.Fatal("expected error for bad poll interval")
	}
	msg := err.Error()
	if !strings.Contains(msg, "IDLE_DECK_POLL_INTERVAL") {
		t.Errorf("error does not name the variable: %s", msg)
	}
	if !strings.Contains(msg, "duration") {
		t.Errorf("error does not name the expected unit: %s", msg)
	}
}

// TestLoadRelaxedValidates verifies H4: LoadRelaxed calls Validate, so `check`
// rejects malformed config rather than printing it and failing later.
// operator-surface §5 step 6 requires `check` to print resolved config, which
// is only meaningful if that config is known to be well-formed.
func TestLoadRelaxedValidates(t *testing.T) {
	clearEnv(t)
	os.Setenv(EnvGitHubToken, "ghp_test")
	os.Setenv(EnvRepos, "acme/widgets")
	os.Setenv(EnvHarnessURL, "https://h.example.com")
	os.Setenv(EnvHarnessToken, "htok")

	// Malformed repos string must fail LoadRelaxed, not pass through.
	os.Setenv(EnvRepos, "acme widgets") // space — invalid per validateRepos
	_, err := LoadRelaxed(nil)
	if err == nil {
		t.Fatal("expected LoadRelaxed to reject malformed repos")
	}
	// The repos error names the offending repo string, not the env var
	// (validateRepos reports the value); what matters is that validation
	// ran at all rather than being skipped.
	if !strings.Contains(err.Error(), "must not contain spaces") {
		t.Errorf("unexpected repos error: %s", err)
	}

	// Malformed harness URL must also fail.
	os.Setenv(EnvRepos, "acme/widgets")
	os.Setenv(EnvHarnessURL, "not a url")
	_, err = LoadRelaxed(nil)
	if err == nil {
		t.Fatal("expected LoadRelaxed to reject malformed harness URL")
	}
	if !strings.Contains(err.Error(), "IDLE_DECK_HARNESS_URL") {
		t.Errorf("error does not name the offending variable: %s", err)
	}

	// A fully valid config must succeed through LoadRelaxed.
	os.Setenv(EnvHarnessURL, "https://h.example.com")
	cfg, err := LoadRelaxed(nil)
	if err != nil {
		t.Fatalf("LoadRelaxed on valid config: %v", err)
	}
	if cfg.Repos != "acme/widgets" {
		t.Errorf("Repos = %q, want acme/widgets", cfg.Repos)
	}
}

func TestFlagOverridesEnv(t *testing.T) {
	clearEnv(t)
	os.Setenv(EnvGitHubToken, "ghp_test")
	os.Setenv(EnvRepos, "acme/widgets")
	os.Setenv(EnvHarnessURL, "https://h.example.com")
	os.Setenv(EnvHarnessToken, "htok")
	os.Setenv("IDLE_DECK_DB", "/env/path")

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fo := RegisterFlags(fs)
	if err := fs.Parse([]string{"--db", "/flag/path"}); err != nil {
		t.Fatalf("fs.Parse: %v", err)
	}

	cfg, err := Load(fo)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DB != "/flag/path" {
		t.Errorf("flag override failed: got %q, want /flag/path", cfg.DB)
	}
}

func TestRedaction(t *testing.T) {
	c := Config{
		GitHubToken:  "ghp_secret",
		HarnessToken: "ht_secret",
		Repos:        "acme/widgets",
		HarnessURL:   "https://h.example.com",
		LogLevel:     "info",
	}
	red := Redact(c)
	if red.GitHubToken != "<redacted>" {
		t.Errorf("GitHubToken redacted = %q, want <redacted>", red.GitHubToken)
	}
	if red.HarnessToken != "<redacted>" {
		t.Errorf("HarnessToken redacted = %q, want <redacted>", red.HarnessToken)
	}
	if red.HarnessURL != "https://h.example.com" {
		t.Errorf("HarnessURL changed by Redact: %q", red.HarnessURL)
	}
	if red.LogLevel != "info" {
		t.Errorf("LogLevel changed by Redact: %q", red.LogLevel)
	}
	// Original unchanged.
	if c.GitHubToken != "ghp_secret" {
		t.Error("Redact mutated its argument")
	}
}

func TestNoEnvFileReader(t *testing.T) {
	// D36: no function in the config package reads a file from the working
	// directory to obtain a token. Scan the package source for forbidden patterns.
	matches, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range matches {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		for _, pat := range []string{".env", "os.ReadFile", "os.Open", "ioutil.ReadFile", "ReadFile"} {
			if strings.Contains(s, pat) {
				// Allow os.ReadFile/ReadFile in yaml.go for reading YAML config files (not .env files)
				if (pat == "os.ReadFile" || pat == "ReadFile") && strings.HasSuffix(f, "yaml.go") {
					continue
				}
				t.Errorf("%s contains %q (D36 violation)", f, pat)
			}
		}
	}
}

func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range allEnvVars() {
		t.Setenv(k, "")
	}
}

func allEnvVars() []string {
	return []string{
		EnvGitHubToken,
		EnvRepos,
		EnvHarnessURL,
		EnvHarnessToken,
		"IDLE_DECK_DB",
		"IDLE_DECK_POLL_INTERVAL",
		"IDLE_DECK_MAX_CONCURRENT_JOBS",
		"IDLE_DECK_LOG_LEVEL",
		"IDLE_DECK_LOG_FORMAT",
		"IDLE_DECK_TIER_TIMEOUT_PLAN",
		"IDLE_DECK_TIER_TIMEOUT_DO",
		"IDLE_DECK_TIER_TIMEOUT_SWEEP",
		"IDLE_DECK_TIER_TIMEOUT_HOTFIX",
		"IDLE_DECK_BUDGET_PLAN",
		"IDLE_DECK_BUDGET_DO",
		"IDLE_DECK_BUDGET_SWEEP",
		"IDLE_DECK_BUDGET_HOTFIX",
		"IDLE_DECK_GITHUB_API",
		"IDLE_DECK_RETRY_BACKOFF_INITIAL",
		"IDLE_DECK_RETRY_BACKOFF_MAX",
		"IDLE_DECK_SWEEP_PERIOD",
		"IDLE_DECK_SWEEP_REPOS",
		"IDLE_DECK_SWEEP_PROMPT",
		"IDLE_DECK_DASHBOARD_ADDR",
		"IDLE_DECK_DASHBOARD_ACTIONS",
		"IDLE_DECK_YIELD_WHEN_BUSY", // D49
	}
}



func TestLoadYAML_Basic(t *testing.T) {
	tmpDir := t.TempDir()
	yamlPath := filepath.Join(tmpDir, "config.yaml")
	err := os.WriteFile(yamlPath, []byte(`
github:
  api: "https://api.custom.com"
harness:
  url: "https://harness.example.com"
poll_interval: "120"
max_concurrent_jobs: 3
log_level: "debug"
log_format: "text"
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadYAML(yamlPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.GitHubAPI != "https://api.custom.com" {
		t.Errorf("GitHubAPI = %q, want %q", cfg.GitHubAPI, "https://api.custom.com")
	}
	if cfg.HarnessURL != "https://harness.example.com" {
		t.Errorf("HarnessURL = %q, want %q", cfg.HarnessURL, "https://harness.example.com")
	}
	if cfg.PollInterval != 120*time.Second {
		t.Errorf("PollInterval = %v, want 120s", cfg.PollInterval)
	}
	if cfg.MaxConcurrent != 3 {
		t.Errorf("MaxConcurrent = %d, want 3", cfg.MaxConcurrent)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want debug", cfg.LogLevel)
	}
	if cfg.LogFormat != "text" {
		t.Errorf("LogFormat = %q, want text", cfg.LogFormat)
	}
}

func TestLoadYAML_NonexistentFile(t *testing.T) {
	cfg, err := LoadYAML("/nonexistent/path/config.yaml")
	if err != nil {
		t.Fatalf("expected nil error for nonexistent file, got %v", err)
	}
	if cfg != nil {
		t.Errorf("expected nil config for nonexistent file, got %+v", cfg)
	}
}

func TestLoadYAML_UnknownKey(t *testing.T) {
	tmpDir := t.TempDir()
	yamlPath := filepath.Join(tmpDir, "config.yaml")
	err := os.WriteFile(yamlPath, []byte(`
unknown_key: "value"
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	_, err = LoadYAML(yamlPath)
	if err == nil {
		t.Fatal("expected error for unknown key")
	}
	if !strings.Contains(err.Error(), "unknown_key") {
		t.Errorf("error %q should mention unknown_key", err.Error())
	}
}

func TestLoadYAML_PrecedenceEnvOverYAML(t *testing.T) {
	tmpDir := t.TempDir()
	yamlPath := filepath.Join(tmpDir, "config.yaml")
	err := os.WriteFile(yamlPath, []byte(`
github:
  api: "https://yaml-api.example.com"
harness:
  url: "https://yaml-harness.example.com"
poll_interval: "120"
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	// Set env var to override YAML
	t.Setenv("IDLE_DECK_HARNESS_URL", "https://env-harness.example.com")
	t.Setenv("IDLE_DECK_GITHUB_TOKEN", "test-token")
	t.Setenv("IDLE_DECK_REPOS", "owner/repo")
	t.Setenv("IDLE_DECK_HARNESS_TOKEN", "test-token")

	fo := &FlagOverrides{ConfigFile: &yamlPath}
	cfg, err := Load(fo)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HarnessURL != "https://env-harness.example.com" {
		t.Errorf("HarnessURL = %q, want env value", cfg.HarnessURL)
	}
	if cfg.GitHubAPI != "https://yaml-api.example.com" {
		t.Errorf("GitHubAPI = %q, want yaml value (not overridden)", cfg.GitHubAPI)
	}
	if cfg.PollInterval != 120*time.Second {
		t.Errorf("PollInterval = %v, want 120s from yaml", cfg.PollInterval)
	}
}

func TestLoadYAML_PrecedenceFlagOverEnvOverYAML(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fo := RegisterFlags(fs)
	
	// Parse with flag
	if err := fs.Parse([]string{"--poll-interval", "60"}); err != nil {
		t.Fatal(err)
	}
	
	tmpDir := t.TempDir()
	yamlPath := filepath.Join(tmpDir, "config.yaml")
	err := os.WriteFile(yamlPath, []byte(`
poll_interval: "120"
`), 0644)
	if err != nil {
		t.Fatal(err)
	}

	t.Setenv("IDLE_DECK_POLL_INTERVAL", "180")
	t.Setenv("IDLE_DECK_GITHUB_TOKEN", "test-token")
	t.Setenv("IDLE_DECK_REPOS", "owner/repo")
	t.Setenv("IDLE_DECK_HARNESS_TOKEN", "test-token")
	t.Setenv("IDLE_DECK_HARNESS_URL", "https://harness.example.com")

	fo.ConfigFile = &yamlPath
	cfg, err := Load(fo)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PollInterval != 60*time.Second {
		t.Errorf("PollInterval = %v, want 60s from flag", cfg.PollInterval)
	}
}
