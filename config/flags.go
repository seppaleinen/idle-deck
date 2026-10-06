package config

import (
	"flag"
	"reflect"
)

// FlagOverrides holds the parsed CLI flags.
// Only non-secret, per-run tunables get flags (D36: tokens are env-only).
// Each field is a pointer to the flag's value; nil pointer = flag not registered.
type FlagOverrides struct {
	fs *flag.FlagSet

	DB                  *string
	Repos               *string
	GitHubAPI           *string
	HarnessURL          *string
	PollInterval        *string
	MaxConcurrent       *string
	LogLevel            *string
	LogFormat           *string
	TierTimeoutPlan     *string
	TierTimeoutDo       *string
	TierTimeoutSweep    *string
	TierTimeoutHotfix   *string
	BudgetPlan          *string
	BudgetDo            *string
	BudgetSweep         *string
	BudgetHotfix        *string
	RetryBackoffInitial *string
	RetryBackoffMax     *string
	SweepPeriod         *string
	SweepRepos          *string
	SweepPrompt         *string
}

// RegisterFlags registers all non-secret CLI flags on fs with zero defaults
// (empty string = not set). It returns the overrides bound to fs.
// Tokens are env-only per D36: flag values leak in `ps`.
func RegisterFlags(fs *flag.FlagSet) *FlagOverrides {
	fo := &FlagOverrides{fs: fs}

	fo.DB = fs.String("db", "", "SQLite database path (env IDLE_DECK_DB)")
	fo.Repos = fs.String("repos", "", "Comma-separated owner/repo list (env IDLE_DECK_REPOS)")
	fo.GitHubAPI = fs.String("github-api", "", "GitHub API base URL (env IDLE_DECK_GITHUB_API)")
	fo.HarnessURL = fs.String("harness-url", "", "Remote execution service base URL (env IDLE_DECK_HARNESS_URL)")
	fo.PollInterval = fs.String("poll-interval", "", "Poll interval in seconds (env IDLE_DECK_POLL_INTERVAL)")
	fo.MaxConcurrent = fs.String("max-concurrent-jobs", "", "Max concurrent jobs (env IDLE_DECK_MAX_CONCURRENT_JOBS)")
	fo.LogLevel = fs.String("log-level", "", "Log level: debug|info|warn|error (env IDLE_DECK_LOG_LEVEL)")
	fo.LogFormat = fs.String("log-format", "", "Log format: json|text (env IDLE_DECK_LOG_FORMAT)")
	fo.TierTimeoutPlan = fs.String("tier-timeout-plan", "", "P1 tier timeout in seconds (env IDLE_DECK_TIER_TIMEOUT_PLAN)")
	fo.TierTimeoutDo = fs.String("tier-timeout-do", "", "P2 tier timeout in seconds (env IDLE_DECK_TIER_TIMEOUT_DO)")
	fo.TierTimeoutSweep = fs.String("tier-timeout-sweep", "", "P3 tier timeout in seconds (env IDLE_DECK_TIER_TIMEOUT_SWEEP)")
	fo.TierTimeoutHotfix = fs.String("tier-timeout-hotfix", "", "P0 tier timeout in seconds (env IDLE_DECK_TIER_TIMEOUT_HOTFIX)")
	fo.BudgetPlan = fs.String("budget-plan", "", "P1 budget in tokens (env IDLE_DECK_BUDGET_PLAN)")
	fo.BudgetDo = fs.String("budget-do", "", "P2 budget in tokens (env IDLE_DECK_BUDGET_DO)")
	fo.BudgetSweep = fs.String("budget-sweep", "", "P3 budget in tokens (env IDLE_DECK_BUDGET_SWEEP)")
	fo.BudgetHotfix = fs.String("budget-hotfix", "", "P0 budget in tokens (env IDLE_DECK_BUDGET_HOTFIX)")
	fo.RetryBackoffInitial = fs.String("retry-backoff-initial", "", "Initial retry backoff in seconds (env IDLE_DECK_RETRY_BACKOFF_INITIAL)")
	fo.RetryBackoffMax = fs.String("retry-backoff-max", "", "Max retry backoff in seconds (env IDLE_DECK_RETRY_BACKOFF_MAX)")
	fo.SweepPeriod = fs.String("sweep-period", "", "Sweep period in seconds; 0 disables (env IDLE_DECK_SWEEP_PERIOD)")
	fo.SweepRepos = fs.String("sweep-repos", "", "Comma-separated sweep repo subset (env IDLE_DECK_SWEEP_REPOS)")
	fo.SweepPrompt = fs.String("sweep-prompt", "", "Sweep prompt (env IDLE_DECK_SWEEP_PROMPT)")

	return fo
}

// ApplyFlags applies explicitly-set flags from fo to cfg.
// It uses flag.Visit to distinguish explicitly-set flags from implicit zero-defaults
// (zero-default flags mean "not set", so they don't override env/defaults).
func ApplyFlags(cfg *Config, fo *FlagOverrides) error {
	if fo == nil || fo.fs == nil {
		return nil
	}

	visited := make(map[string]bool)
	fo.fs.Visit(func(f *flag.Flag) { visited[f.Name] = true })

	for _, f := range fields {
		if f.flag == "" {
			continue
		}
		if !visited[f.flag] {
			continue
		}
		ptr := reflect.ValueOf(fo).Elem().FieldByName(f.name)
		if !ptr.IsValid() || ptr.IsNil() {
			continue
		}
		if err := setField(reflect.ValueOf(cfg).Elem(), f, ptr.Elem().String()); err != nil {
			return err
		}
	}
	return nil
}
