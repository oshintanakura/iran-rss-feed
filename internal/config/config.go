// Package config loads and validates the single YAML configuration file
// that drives the whole program.
package config

import (
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

type TelegramMTProto struct {
	APIID       string `yaml:"api_id"`
	APIHash     string `yaml:"api_hash"`
	SessionFile string `yaml:"session_file"`
}

type Source struct {
	Mode     string          `yaml:"mode"` // "web" | "mtproto"
	Telegram TelegramMTProto `yaml:"telegram"`
}

type Translate struct {
	APIKey          string  `yaml:"api_key"`
	BaseURL         string  `yaml:"base_url"`
	Model           string  `yaml:"model"`
	MaxCharsPerPost int     `yaml:"max_chars_per_post"`
	TargetLanguage  string  `yaml:"target_language"`
	Temperature     float64 `yaml:"temperature"`
}

type Output struct {
	Dir             string `yaml:"dir"`
	BaseURL         string `yaml:"base_url"`
	PerChannelFeeds bool   `yaml:"per_channel_feeds"`
	CombinedFeed    bool   `yaml:"combined_feed"`
	MaxFeedAgeDays  int    `yaml:"max_feed_age_days"`  // every translated post newer than this is kept in its feed, no matter how many that is
	MaxItemsPerFeed int    `yaml:"max_items_per_feed"` // hard ceiling on feed size, purely against a pathological runaway — should never actually bind
	IncludeOriginal bool   `yaml:"include_original"`
}

type State struct {
	Path string `yaml:"path"`
}

type Runtime struct {
	FetchTimeoutSeconds     int  `yaml:"fetch_timeout_seconds"`
	TranslateTimeoutSeconds int  `yaml:"translate_timeout_seconds"`
	MaxNewPostsPerRun       int  `yaml:"max_new_posts_per_run"`
	MaxPostAgeDays          int  `yaml:"max_post_age_days"` // ignore posts older than this, every fetch, not just first backfill
	DryRun                  bool `yaml:"dry_run"`
}

// Channel is one source channel: its Telegram username (no @, no
// t.me/) and the category its posts fall under for the merged category
// feeds.
type Channel struct {
	Name     string `yaml:"name"`
	Category string `yaml:"category"` // defaults to "analysis"
}

// Config is the root of config.yaml.
type Config struct {
	// Channels lists the channels to poll, each with its category.
	// To stop polling a channel, remove its line from the list.
	Channels  []Channel `yaml:"channels"`
	Source    Source    `yaml:"source"`
	Translate Translate `yaml:"translate"`
	Output    Output    `yaml:"output"`
	State     State     `yaml:"state"`
	Runtime   Runtime   `yaml:"runtime"`
}

var envPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// categoryNamePattern keeps category names safe as feed filenames.
var categoryNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// defaultCategory is the category channels fall into when their config
// line doesn't say otherwise.
const defaultCategory = "analysis"

// ChannelNames returns the configured channel names, in config order.
func (cfg *Config) ChannelNames() []string {
	names := make([]string, len(cfg.Channels))
	for i, c := range cfg.Channels {
		names[i] = c.Name
	}
	return names
}

// Categories returns the distinct channel categories present, sorted.
func (cfg *Config) Categories() []string {
	seen := make(map[string]bool, len(cfg.Channels))
	var cats []string
	for _, c := range cfg.Channels {
		if !seen[c.Category] {
			seen[c.Category] = true
			cats = append(cats, c.Category)
		}
	}
	sort.Strings(cats)
	return cats
}

// expandEnv replaces ${VAR_NAME} with the value of the environment
// variable VAR_NAME. Unset variables expand to an empty string, exactly
// like os.Expand/shell behavior, so downstream validation catches the
// resulting empty value.
func expandEnv(s string) string {
	return envPattern.ReplaceAllStringFunc(s, func(m string) string {
		name := envPattern.FindStringSubmatch(m)[1]
		return os.Getenv(name)
	})
}

// Load reads, expands, and validates the config at path.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config %s: %w", path, err)
	}

	expanded := expandEnv(string(raw))

	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("parsing config %s: %w", path, err)
	}

	applyDefaults(&cfg)

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	return &cfg, nil
}

func applyDefaults(cfg *Config) {
	for i := range cfg.Channels {
		if cfg.Channels[i].Category == "" {
			cfg.Channels[i].Category = defaultCategory
		}
	}
	if cfg.Source.Mode == "" {
		cfg.Source.Mode = "web"
	}
	if cfg.Translate.MaxCharsPerPost <= 0 {
		// 4096 is Telegram's own hard limit on a single message's text
		// length, so this never actually bounds a legitimate post short —
		// it's a safety net against something unexpected, not a real cap.
		cfg.Translate.MaxCharsPerPost = 4096
	}
	if cfg.Translate.TargetLanguage == "" {
		cfg.Translate.TargetLanguage = "English"
	}
	if cfg.Output.Dir == "" {
		cfg.Output.Dir = "./public/feeds"
	}
	if cfg.Output.MaxFeedAgeDays <= 0 {
		cfg.Output.MaxFeedAgeDays = 10
	}
	if cfg.Output.MaxItemsPerFeed <= 0 {
		// Comfortably above any realistic volume within max_feed_age_days
		// across every configured channel, so it never actually trims the
		// age window in practice — it's a circuit breaker, not the real
		// limit.
		cfg.Output.MaxItemsPerFeed = 2000
	}
	if cfg.State.Path == "" {
		cfg.State.Path = "./state.db"
	}
	if cfg.Runtime.FetchTimeoutSeconds <= 0 {
		cfg.Runtime.FetchTimeoutSeconds = 30
	}
	if cfg.Runtime.TranslateTimeoutSeconds <= 0 {
		cfg.Runtime.TranslateTimeoutSeconds = 120
	}
	if cfg.Runtime.MaxNewPostsPerRun <= 0 {
		cfg.Runtime.MaxNewPostsPerRun = 200
	}
	if cfg.Runtime.MaxPostAgeDays <= 0 {
		cfg.Runtime.MaxPostAgeDays = 7
	}
}

// Validate performs the fatal startup checks the spec requires.
func (cfg *Config) Validate() error {
	if len(cfg.Channels) == 0 {
		return fmt.Errorf("config: no channels — add at least one entry under 'channels'")
	}
	seen := make(map[string]bool, len(cfg.Channels))
	for _, c := range cfg.Channels {
		if strings.TrimSpace(c.Name) == "" {
			return fmt.Errorf("config: 'channels' contains an entry with a blank name")
		}
		if seen[c.Name] {
			return fmt.Errorf("config: channel %q is listed more than once", c.Name)
		}
		seen[c.Name] = true
		if !categoryNamePattern.MatchString(c.Category) {
			return fmt.Errorf("config: channel %q has category %q — category names must match %s", c.Name, c.Category, categoryNamePattern)
		}
	}

	if cfg.Translate.APIKey == "" {
		return fmt.Errorf("config: translate.api_key is empty after environment expansion — set it (directly, or via an env var) before running")
	}
	if cfg.Translate.BaseURL == "" {
		return fmt.Errorf("config: translate.base_url is empty after environment expansion — set it to your chat-completions API's base URL")
	}
	if cfg.Translate.Model == "" {
		return fmt.Errorf("config: translate.model is empty after environment expansion — set it to the model name your provider expects")
	}

	if cfg.Source.Mode != "web" && cfg.Source.Mode != "mtproto" {
		return fmt.Errorf("config: source.mode must be \"web\" or \"mtproto\", got %q", cfg.Source.Mode)
	}

	return nil
}
