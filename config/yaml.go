package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// yamlConfig mirrors Config but with YAML-tagged fields for unmarshaling.
type yamlConfig struct {
	GitHub              yamlGitHub              `yaml:"github,omitempty"`
	Harness             yamlHarness             `yaml:"harness,omitempty"`
	DB                  string                  `yaml:"db,omitempty"`
	PollInterval        string                  `yaml:"poll_interval,omitempty"`
	MaxConcurrent       int                     `yaml:"max_concurrent_jobs,omitempty"`
	LogLevel            string                  `yaml:"log_level,omitempty"`
	LogFormat           string                  `yaml:"log_format,omitempty"`
	TierTimeoutPlan     string                  `yaml:"tier_timeout_plan,omitempty"`
	TierTimeoutDo       string                  `yaml:"tier_timeout_do,omitempty"`
	TierTimeoutSweep    string                  `yaml:"tier_timeout_sweep,omitempty"`
	TierTimeoutHotfix   string                  `yaml:"tier_timeout_hotfix,omitempty"`
	BudgetPlan          int                     `yaml:"budget_plan,omitempty"`
	BudgetDo            int                     `yaml:"budget_do,omitempty"`
	BudgetSweep         int                     `yaml:"budget_sweep,omitempty"`
	BudgetHotfix        int                     `yaml:"budget_hotfix,omitempty"`
	GitHubAPI           string                  `yaml:"github_api,omitempty"`
	RetryBackoffInitial string                  `yaml:"retry_backoff_initial,omitempty"`
	RetryBackoffMax     string                  `yaml:"retry_backoff_max,omitempty"`
	SweepPeriod         string                  `yaml:"sweep_period,omitempty"`
	SweepRepos          string                  `yaml:"sweep_repos,omitempty"`
	SweepPrompt         string                  `yaml:"sweep_prompt,omitempty"`
	DashboardAddr       string                  `yaml:"dashboard_addr,omitempty"`
	DashboardActions    bool                    `yaml:"dashboard_actions,omitempty"`
	YieldWhenBusy       bool                    `yaml:"yield_when_busy,omitempty"`
	UnknownFields       map[string]interface{}  `yaml:",inline"`
}

type yamlGitHub struct {
	Token string `yaml:"token,omitempty"`
	API   string `yaml:"api,omitempty"`
}

type yamlHarness struct {
	URL   string `yaml:"url,omitempty"`
	Token string `yaml:"token,omitempty"`
}

// LoadYAML loads configuration from a YAML file at path.
// Returns nil if file doesn't exist (not an error — YAML is optional).
// Returns error if file exists but is invalid, or has unknown keys.
func LoadYAML(path string) (*Config, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil, nil // Not an error — YAML is optional
	} else if err != nil {
		return nil, fmt.Errorf("yaml: stat %s: %w", path, err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("yaml: read %s: %w", path, err)
	}

	var yc yamlConfig
	if err := yaml.Unmarshal(data, &yc); err != nil {
		return nil, fmt.Errorf("yaml: parse %s: %w", path, err)
	}

	// Check for unknown fields
	if len(yc.UnknownFields) > 0 {
		keys := make([]string, 0, len(yc.UnknownFields))
		for k := range yc.UnknownFields {
			keys = append(keys, k)
		}
		return nil, fmt.Errorf("yaml: unknown keys in %s: %s", path, strings.Join(keys, ", "))
	}

	cfg := &Config{}
	root := reflect.ValueOf(cfg).Elem()

	// Apply YAML values
	if yc.GitHub.Token != "" {
		cfg.GitHubToken = yc.GitHub.Token
	}
	if yc.GitHub.API != "" {
		cfg.GitHubAPI = yc.GitHub.API
	}
	if yc.Harness.URL != "" {
		cfg.HarnessURL = yc.Harness.URL
	}
	if yc.Harness.Token != "" {
		cfg.HarnessToken = yc.Harness.Token
	}
	if yc.DB != "" {
		cfg.DB = yc.DB
	}
	if yc.PollInterval != "" {
		if err := setField(root, findField("PollInterval"), yc.PollInterval); err != nil {
			return nil, fmt.Errorf("yaml: poll_interval: %w", err)
		}
	}
	if yc.MaxConcurrent != 0 {
		cfg.MaxConcurrent = yc.MaxConcurrent
	}
	if yc.LogLevel != "" {
		cfg.LogLevel = yc.LogLevel
	}
	if yc.LogFormat != "" {
		cfg.LogFormat = yc.LogFormat
	}
	if yc.TierTimeoutPlan != "" {
		if err := setField(root, findField("TierTimeoutPlan"), yc.TierTimeoutPlan); err != nil {
			return nil, fmt.Errorf("yaml: tier_timeout_plan: %w", err)
		}
	}
	if yc.TierTimeoutDo != "" {
		if err := setField(root, findField("TierTimeoutDo"), yc.TierTimeoutDo); err != nil {
			return nil, fmt.Errorf("yaml: tier_timeout_do: %w", err)
		}
	}
	if yc.TierTimeoutSweep != "" {
		if err := setField(root, findField("TierTimeoutSweep"), yc.TierTimeoutSweep); err != nil {
			return nil, fmt.Errorf("yaml: tier_timeout_sweep: %w", err)
		}
	}
	if yc.TierTimeoutHotfix != "" {
		if err := setField(root, findField("TierTimeoutHotfix"), yc.TierTimeoutHotfix); err != nil {
			return nil, fmt.Errorf("yaml: tier_timeout_hotfix: %w", err)
		}
	}
	cfg.BudgetPlan = yc.BudgetPlan
	cfg.BudgetDo = yc.BudgetDo
	cfg.BudgetSweep = yc.BudgetSweep
	cfg.BudgetHotfix = yc.BudgetHotfix
	if yc.GitHubAPI != "" {
		cfg.GitHubAPI = yc.GitHubAPI
	}
	if yc.RetryBackoffInitial != "" {
		if err := setField(root, findField("RetryBackoffInitial"), yc.RetryBackoffInitial); err != nil {
			return nil, fmt.Errorf("yaml: retry_backoff_initial: %w", err)
		}
	}
	if yc.RetryBackoffMax != "" {
		if err := setField(root, findField("RetryBackoffMax"), yc.RetryBackoffMax); err != nil {
			return nil, fmt.Errorf("yaml: retry_backoff_max: %w", err)
		}
	}
	if yc.SweepPeriod != "" {
		if err := setField(root, findField("SweepPeriod"), yc.SweepPeriod); err != nil {
			return nil, fmt.Errorf("yaml: sweep_period: %w", err)
		}
	}
	if yc.SweepRepos != "" {
		cfg.SweepRepos = yc.SweepRepos
	}
	if yc.SweepPrompt != "" {
		cfg.SweepPrompt = yc.SweepPrompt
	}
	if yc.DashboardAddr != "" {
		cfg.DashboardAddr = yc.DashboardAddr
	}
	if yc.DashboardActions {
		cfg.DashboardActions = yc.DashboardActions
	}
	if yc.YieldWhenBusy {
		cfg.YieldWhenBusy = yc.YieldWhenBusy
	}

	return cfg, nil
}

// findField returns the fieldMeta for the given field name.
func findField(name string) fieldMeta {
	for _, f := range fields {
		if f.name == name {
			return f
		}
	}
	return fieldMeta{}
}

// DefaultConfigPaths returns the standard config file locations in precedence order.
// The first existing file is used.
func DefaultConfigPaths() []string {
	home, _ := os.UserHomeDir()
	return []string{
		"idle-deck.yaml", // working directory
		filepath.Join(home, ".config", "idle-deck", "config.yaml"),
		"/etc/idle-deck/config.yaml",
	}
}

// FindConfigFile returns the first existing config file path, or "" if none exist.
func FindConfigFile() string {
	for _, p := range DefaultConfigPaths() {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}
