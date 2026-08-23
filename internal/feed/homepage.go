package feed

import (
	"fmt"
	"html/template"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const siteDescription = "An independent English digest of public Telegram posts from Iranian political figures and commentators, machine-translated and updated every few hours."

var homepageTmpl = template.Must(template.New("homepage").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Iran Telegram Digest</title>
<meta name="description" content="{{.Description}}">
<meta name="keywords" content="Iran, Iranian politics, Telegram, Persian to English translation, Farsi translation, Iran news, Iranian opposition, Iran RSS feed, Iranian political commentary">
{{if .SiteURL}}<link rel="canonical" href="{{.SiteURL}}/">
<meta property="og:type" content="website">
<meta property="og:title" content="Iran Telegram Digest">
<meta property="og:description" content="{{.Description}}">
<meta property="og:url" content="{{.SiteURL}}/">
<meta name="twitter:card" content="summary">
{{end}}<link rel="alternate" type="application/atom+xml" title="Iran Telegram Digest — combined feed" href="feeds/all.xml">
<style>
  :root {
    color-scheme: light dark;
    --bg: #ffffff;
    --text: #1f2328;
    --muted: #5b6470;
    --link: #0a58ca;
    --rule: #e3e6ea;
  }
  @media (prefers-color-scheme: dark) {
    :root {
      --bg: #0d1117;
      --text: #e6edf3;
      --muted: #9aa4b2;
      --link: #6cb6ff;
      --rule: #2a2f37;
    }
  }
  * { box-sizing: border-box; }
  body {
    background: var(--bg);
    color: var(--text);
    font-family: -apple-system, system-ui, "Segoe UI", Roboto, sans-serif;
    max-width: 640px;
    margin: 3rem auto;
    padding: 0 1.5rem;
    line-height: 1.7;
    font-size: 1.08rem;
  }
  h1 { font-size: 1.5rem; margin-bottom: 1.5rem; }
  h2 { font-size: 1.1rem; margin-top: 2rem; }
  p { margin: 1.2rem 0; }
  .disclaimer { color: var(--muted); font-size: 0.95rem; }
  .feed-link {
    display: inline-block;
    margin-top: 0.5rem;
    padding: 0.5rem 1rem;
    border: 1px solid var(--link);
    border-radius: 6px;
    text-decoration: none;
    font-weight: 600;
  }
  a { color: var(--link); }
  ol.channels {
    columns: 2;
    column-gap: 2rem;
    padding-left: 1.5rem;
    color: var(--muted);
    font-size: 0.95rem;
  }
  ol.channels li { break-inside: avoid; margin-bottom: 0.3rem; }
  ol.channels a { color: var(--muted); text-decoration: none; }
  ol.channels a:hover { text-decoration: underline; }
  hr { border: none; border-top: 1px solid var(--rule); margin: 2.5rem 0; }
  .hope { font-style: italic; color: var(--muted); text-align: center; }
</style>
</head>
<body>
<h1>Iran Telegram Digest</h1>

<p>This site gathers public posts from a number of Iranian political
figures and commentators — most of whom publish primarily on Telegram —
and machine-translates them to English.</p>

<p class="disclaimer">Inclusion here does not imply any correspondence,
endorsement, or agreement from the individuals or channels listed. This
is simply a tool to help follow their public points of view.</p>

<p><a class="feed-link" href="feeds/all.xml">RSS feed</a></p>

<h2>Channels included</h2>
<ol class="channels">
{{range .Channels}}  <li><a href="feeds/{{.}}.xml">{{.}}</a></li>
{{end}}</ol>

<hr>

<p class="hope">In hope of peace and freedom for Iran.</p>
</body>
</html>
`))

type homepageData struct {
	Description string
	SiteURL     string
	Channels    []string
}

func sortedChannels(channels []string) []string {
	sorted := make([]string, len(channels))
	copy(sorted, channels)
	sort.Slice(sorted, func(i, j int) bool {
		return strings.ToLower(sorted[i]) < strings.ToLower(sorted[j])
	})
	return sorted
}

// WriteHomepage renders the static homepage into publicDir/index.html,
// atomically (temp file + rename). The channel list is always
// regenerated from the current config, alphabetically, so it never goes
// stale — unlike the rest of the page's hand-written copy, which lives
// only in this template, not read from anywhere at runtime. siteURL is
// the site's root (no trailing slash, no "/feeds"); pass "" to omit the
// canonical/Open Graph tags that need an absolute URL.
func WriteHomepage(publicDir, siteURL string, channels []string) error {
	data := homepageData{
		Description: siteDescription,
		SiteURL:     strings.TrimRight(siteURL, "/"),
		Channels:    sortedChannels(channels),
	}

	if err := os.MkdirAll(publicDir, 0o755); err != nil {
		return fmt.Errorf("creating public dir %s: %w", publicDir, err)
	}

	final := filepath.Join(publicDir, "index.html")
	tmp := final + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("creating %s: %w", tmp, err)
	}
	if err := homepageTmpl.Execute(f, data); err != nil {
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
