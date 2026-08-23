package feed

import (
	"fmt"
	"os"
	"path/filepath"
	"text/template"
)

// aiCrawlerUserAgents are named explicitly (rather than relying only on
// the wildcard rule below) so intent is unambiguous: this site wants to
// be crawled and cited by search engines and LLM answer/search tools,
// not just tolerate it by omission.
var aiCrawlerUserAgents = []string{
	"GPTBot",
	"ChatGPT-User",
	"Google-Extended",
	"ClaudeBot",
	"anthropic-ai",
	"PerplexityBot",
	"CCBot",
	"Bingbot",
}

var robotsTmpl = template.Must(template.New("robots").Parse(`User-agent: *
Allow: /
{{range .AICrawlers}}
User-agent: {{.}}
Allow: /
{{end}}
{{if .SiteURL}}Sitemap: {{.SiteURL}}/sitemap.xml
{{end}}`))

type robotsData struct {
	SiteURL    string
	AICrawlers []string
}

// WriteRobotsTxt writes a robots.txt that explicitly allows every
// crawler, including well-known AI/LLM ones by name, plus a Sitemap
// pointer if siteURL is known.
func WriteRobotsTxt(publicDir, siteURL string) error {
	if err := os.MkdirAll(publicDir, 0o755); err != nil {
		return fmt.Errorf("creating public dir %s: %w", publicDir, err)
	}

	final := filepath.Join(publicDir, "robots.txt")
	tmp := final + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("creating %s: %w", tmp, err)
	}
	data := robotsData{SiteURL: siteURL, AICrawlers: aiCrawlerUserAgents}
	if err := robotsTmpl.Execute(f, data); err != nil {
		f.Close()
		return fmt.Errorf("rendering %s: %w", final, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, final); err != nil {
		return fmt.Errorf("renaming %s to %s: %w", tmp, final, err)
	}
	return nil
}

var sitemapTmpl = template.Must(template.New("sitemap").Parse(`<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>{{.SiteURL}}/</loc></url>
  <url><loc>{{.SiteURL}}/feeds/all.xml</loc></url>
{{range .Channels}}  <url><loc>{{$.SiteURL}}/feeds/{{.}}.xml</loc></url>
{{end}}</urlset>
`))

type sitemapData struct {
	SiteURL  string
	Channels []string
}

// WriteSitemap writes a sitemap.xml listing the homepage, the combined
// feed, and every per-channel feed — search engines follow the feeds
// from there to discover individual post pages, so those aren't listed
// directly (there's no bound on how many accumulate over time).
// siteURL must be non-empty; the sitemap is meaningless without it.
func WriteSitemap(publicDir, siteURL string, channels []string) error {
	if siteURL == "" {
		return nil
	}

	if err := os.MkdirAll(publicDir, 0o755); err != nil {
		return fmt.Errorf("creating public dir %s: %w", publicDir, err)
	}

	final := filepath.Join(publicDir, "sitemap.xml")
	tmp := final + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("creating %s: %w", tmp, err)
	}
	data := sitemapData{SiteURL: siteURL, Channels: sortedChannels(channels)}
	if err := sitemapTmpl.Execute(f, data); err != nil {
		f.Close()
		return fmt.Errorf("rendering %s: %w", final, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, final); err != nil {
		return fmt.Errorf("renaming %s to %s: %w", tmp, final, err)
	}
	return nil
}
