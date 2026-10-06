package config

import (
	"errors"
	"flag"
	"os"
	"path/filepath"
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
	}
}
