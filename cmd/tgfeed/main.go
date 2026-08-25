// Command tgfeed fetches new posts from a list of public Persian
// Telegram channels, translates them to English with a chat-completions
// API, and writes Atom feeds to disk. See README.md for usage.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"iran-rss-feed/internal/config"
	"iran-rss-feed/internal/feed"
	"iran-rss-feed/internal/source"
	"iran-rss-feed/internal/store"
	"iran-rss-feed/internal/translate"
)

func main() {
	os.Exit(run())
}

func run() int {
	configPath := flag.String("config", "./config.yaml", "path to config.yaml")
	_ = flag.Bool("once", true, "run one cycle and exit (the default and only mode)")
	dryRunFlag := flag.Bool("dry-run", false, "override config: fetch + report, never call the chat API, never write XML")
	onlyChannel := flag.String("channel", "", "restrict this run to one channel, for debugging")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Error("startup failed", "error", err)
		return 1
	}
	if *dryRunFlag {
		cfg.Runtime.DryRun = true
	}

	channels := cfg.Channels
	if *onlyChannel != "" && !slices.Contains(channels, *onlyChannel) {
		logger.Error("startup failed", "error", fmt.Sprintf("--channel %q is not in config", *onlyChannel))
		return 1
	}

	src, err := buildSource(cfg)
	if err != nil {
		logger.Error("startup failed", "error", err)
		return 1
	}

	st, err := store.Open(cfg.State.Path)
	if err != nil {
		logger.Error("startup failed", "error", err)
		return 1
	}
	defer st.Close()

	translator := translate.NewClient(
		cfg.Translate.BaseURL, cfg.Translate.APIKey, cfg.Translate.Model,
		cfg.Translate.Temperature,
		time.Duration(cfg.Runtime.TranslateTimeoutSeconds)*time.Second,
		logger,
	)

	ctx := context.Background()
	start := time.Now()

	var attempted, succeeded, totalNew, totalTranslated, totalFailed int
	remainingBudget := cfg.Runtime.MaxNewPostsPerRun

	for _, ch := range channels {
		if *onlyChannel != "" && ch != *onlyChannel {
			continue
		}
		attempted++

		newCount, translatedCount, failedCount, err := processChannel(ctx, cfg, src, st, translator, ch, &remainingBudget, logger)
		if err != nil {
			logger.Error("channel fetch failed", "channel", ch, "error", err)
			continue
		}

		succeeded++
		totalNew += newCount
		totalTranslated += translatedCount
		totalFailed += failedCount
	}

	// Feeds are regenerated from whatever is already in the store,
	// covering every enabled channel regardless of --channel or of
	// per-channel fetch failures above.
	if cfg.Runtime.DryRun {
		logger.Info("dry run: skipping feed writing")
	} else {
		writeFeeds(ctx, cfg, st, channels, logger)
	}

	logger.Info("run summary",
		"channels_total", len(channels),
		"channels_attempted", attempted,
		"channels_succeeded", succeeded,
		"new", totalNew,
		"translated", totalTranslated,
		"failed", totalFailed,
		"api_calls", translator.Calls,
		"wall_time", time.Since(start).String(),
	)

	if attempted > 0 && succeeded == 0 {
		return 1
	}
	return 0
}

func buildSource(cfg *config.Config) (source.Source, error) {
	timeout := time.Duration(cfg.Runtime.FetchTimeoutSeconds) * time.Second
	switch cfg.Source.Mode {
	case "mtproto":
		return source.NewMTProtoSource(
			cfg.Source.Telegram.APIID,
			cfg.Source.Telegram.APIHash,
			cfg.Source.Telegram.SessionFile,
		)
	default: // "web"
		return source.NewWebSource(timeout), nil
	}
}

// processChannel fetches, filters, and translates one channel's posts.
// It returns how many were new/translated/failed so the caller can log a
// per-channel summary line and roll up run totals.
func processChannel(
	ctx context.Context,
	cfg *config.Config,
	src source.Source,
	st *store.Store,
	translator *translate.Client,
	ch string,
	remainingBudget *int,
	logger *slog.Logger,
) (newCount, translatedCount, failedCount int, err error) {
	fetchCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.Runtime.FetchTimeoutSeconds)*time.Second)
	posts, err := src.Fetch(fetchCtx, ch)
	cancel()
	if err != nil {
		return 0, 0, 0, err
	}

	recentPosts, dropped := filterRecent(posts, cfg.Runtime.MaxPostAgeDays)
	if dropped > 0 {
		logger.Info("dropped posts older than max_post_age_days", "channel", ch, "dropped", dropped, "max_post_age_days", cfg.Runtime.MaxPostAgeDays)
	}

	newPosts, err := st.FilterUnseen(ctx, recentPosts)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("checking seen state: %w", err)
	}

	if *remainingBudget <= 0 {
		if len(newPosts) > 0 {
			logger.Warn("max_new_posts_per_run reached, skipping this channel's new posts", "channel", ch, "new", len(newPosts))
		}
		newPosts = nil
	} else if len(newPosts) > *remainingBudget {
		logger.Warn("max_new_posts_per_run reached mid-channel, truncating", "channel", ch, "wanted", len(newPosts), "allowed", *remainingBudget)
		newPosts = newPosts[:*remainingBudget]
	}
	*remainingBudget -= len(newPosts)

	if cfg.Runtime.DryRun {
		chars := 0
		for _, p := range newPosts {
			chars += len(p.Text)
		}
		logger.Info("dry run: would translate", "channel", ch, "count", len(newPosts), "estimated_chars", chars)
		logger.Info("channel processed", "channel", ch, "fetched", len(posts), "new", len(newPosts), "translated", 0, "failed", 0)
		return len(newPosts), 0, 0, nil
	}

	translatedCount, failedCount = translatePosts(ctx, cfg, st, translator, newPosts, logger)

	logger.Info("channel processed", "channel", ch, "fetched", len(posts), "new", len(newPosts), "translated", translatedCount, "failed", failedCount)
	return len(newPosts), translatedCount, failedCount, nil
}

// translatePosts skips over-length posts, batches the rest, and saves
// every result (translated, skipped-too-long, or failed) to the store.
func translatePosts(
	ctx context.Context,
	cfg *config.Config,
	st *store.Store,
	translator *translate.Client,
	posts []source.Post,
	logger *slog.Logger,
) (translatedCount, failedCount int) {
	var eligible []source.Post
	for _, p := range posts {
		if len(p.Text) > cfg.Translate.MaxCharsPerPost {
			if err := st.SaveSkippedTooLong(ctx, p); err != nil {
				logger.Error("saving skipped-too-long post failed", "channel", p.Channel, "message_id", p.MessageID, "error", err)
			}
			continue
		}
		eligible = append(eligible, p)
	}

	for _, batch := range chunk(eligible, cfg.Translate.BatchSize) {
		items := make([]translate.Item, len(batch))
		for i, p := range batch {
			items[i] = translate.Item{ID: strconv.FormatInt(p.MessageID, 10), Text: p.Text}
		}

		translateCtx, cancel := context.WithTimeout(ctx, time.Duration(cfg.Runtime.TranslateTimeoutSeconds)*time.Second)
		results, err := translator.Batch(translateCtx, items)
		cancel()
		if err != nil {
			logger.Error("batch translation failed outright", "channel", batch[0].Channel, "count", len(batch), "error", err)
			results = map[string]string{}
		}

		for _, p := range batch {
			text, ok := results[strconv.FormatInt(p.MessageID, 10)]
			if !ok || text == "" {
				if err := st.SaveFailed(ctx, p); err != nil {
					logger.Error("saving failed post failed", "channel", p.Channel, "message_id", p.MessageID, "error", err)
				}
				logger.Warn("translation missing for post, will retry next run", "channel", p.Channel, "message_id", p.MessageID)
				failedCount++
				continue
			}
			if err := st.SaveTranslated(ctx, p, text); err != nil {
				logger.Error("saving translated post failed", "channel", p.Channel, "message_id", p.MessageID, "error", err)
				failedCount++
				continue
			}
			translatedCount++
		}
	}

	return translatedCount, failedCount
}

// filterRecent drops posts older than maxAgeDays, so a channel that
// posts rarely (or one just added) never backfills months- or years-old
// content — this runs on every fetch, not just the first one.
func filterRecent(posts []source.Post, maxAgeDays int) (recent []source.Post, dropped int) {
	if maxAgeDays <= 0 {
		return posts, 0
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -maxAgeDays)
	for _, p := range posts {
		if p.PostedAt.Before(cutoff) {
			dropped++
			continue
		}
		recent = append(recent, p)
	}
	return recent, dropped
}

func chunk(posts []source.Post, size int) [][]source.Post {
	if size <= 0 {
		size = len(posts)
	}
	var chunks [][]source.Post
	for i := 0; i < len(posts); i += size {
		end := i + size
		if end > len(posts) {
			end = len(posts)
		}
		chunks = append(chunks, posts[i:end])
	}
	return chunks
}

func writeFeeds(ctx context.Context, cfg *config.Config, st *store.Store, channels []string, logger *slog.Logger) {
	opts := feed.Options{BaseURL: cfg.Output.BaseURL, IncludeOriginal: cfg.Output.IncludeOriginal}

	if cfg.Output.PerChannelFeeds {
		for _, ch := range channels {
			items, err := st.Recent(ctx, ch, cfg.Output.MaxFeedAgeDays, cfg.Output.MaxItemsPerFeed)
			if err != nil {
				logger.Error("reading recent posts failed", "channel", ch, "error", err)
				continue
			}
			if err := feed.Write(cfg.Output.Dir, ch, ch, items, opts); err != nil {
				logger.Error("writing feed failed", "channel", ch, "error", err)
			}
		}
	}

	if cfg.Output.CombinedFeed {
		items, err := st.RecentAll(ctx, cfg.Output.MaxFeedAgeDays, cfg.Output.MaxItemsPerFeed)
		if err != nil {
			logger.Error("reading combined recent posts failed", "error", err)
		} else if err := feed.Write(cfg.Output.Dir, "all", "All channels", items, opts); err != nil {
			logger.Error("writing combined feed failed", "error", err)
		}
	}

	// One standalone page per translated post ever stored (not just what's
	// in the feed window), so feed item links have somewhere permanent to
	// point. Never pruned — old pages just stay as an archive.
	allItems, err := st.AllTranslated(ctx)
	if err != nil {
		logger.Error("reading all translated posts for post pages failed", "error", err)
		return
	}
	if err := feed.WritePostPages(cfg.Output.Dir, allItems, opts); err != nil {
		logger.Error("writing post pages failed", "error", err)
	}

	// output.dir is the feeds subdirectory (e.g. ./public/feeds); the
	// homepage, robots.txt, and sitemap.xml live one level up, alongside
	// it. siteURL is output.base_url with the trailing "/feeds" removed.
	publicDir := filepath.Dir(cfg.Output.Dir)
	siteURL := strings.TrimSuffix(strings.TrimRight(cfg.Output.BaseURL, "/"), "/feeds")

	if err := feed.WriteHomepage(publicDir, siteURL, channels); err != nil {
		logger.Error("writing homepage failed", "error", err)
	}
	if err := feed.WriteRobotsTxt(publicDir, siteURL); err != nil {
		logger.Error("writing robots.txt failed", "error", err)
	}
	if err := feed.WriteSitemap(publicDir, siteURL, channels); err != nil {
		logger.Error("writing sitemap.xml failed", "error", err)
	}
}
