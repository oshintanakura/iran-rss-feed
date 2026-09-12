package feed

import (
	"fmt"
	"html"
	"html/template"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const siteDescription = "An independent English digest of public Telegram posts from Iranian political figures and commentators, machine-translated and updated every few hours."

// HomepagePostsPerChannel caps how many posts the homepage lists per
// channel, keeping the page a light index rather than a full archive.
const HomepagePostsPerChannel = 10

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
  h3 { font-size: 1.05rem; margin: 1.25rem 0 0.25rem; }
  p { margin: 1.2rem 0; }
  .disclaimer { color: var(--muted); font-size: 0.95rem; }
  ul.posts { list-style: none; padding: 0; margin: 0; }
  ul.posts li { margin-bottom: 0.2rem; }
  .post-date { color: var(--muted); font-size: 0.85rem; margin-right: 0.5rem; }
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

<h2>Latest posts by channel</h2>
{{range .ChannelSections}}{{if .Posts}}
<h3>@{{.Name}}</h3>
<ul class="posts">
{{range .Posts}}  <li><span class="post-date">{{.Date}}</span><a href="{{.URL}}">{{.Title}}</a></li>
{{end}}</ul>
{{end}}{{end}}

<h2>RSS feeds</h2>
<ol class="channels">
{{range .Categories}}  <li><a href="feeds/{{.}}.xml">{{.}} (category)</a></li>
{{end}}{{range .Channels}}  <li><a href="feeds/{{.}}.xml">{{.}}</a></li>
{{end}}</ol>

<hr>

<p class="hope">In hope of peace and freedom for Iran.</p>
</body>
</html>
`))

type homepageData struct {
	Description     string
	SiteURL         string
	Channels        []string
	Categories      []string
	ChannelSections []homepageChannel
}

// homepageChannel is one writer's section on the homepage: their latest
// few posts as plain HTML links, so crawlers and readers have a real
// HTML path into the post pages (not just the RSS feeds).
type homepageChannel struct {
	Name  string
	Posts []homepagePost
}

type homepagePost struct {
	Date  string
	Title string
	URL   string
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
// atomically (temp file + rename). The channel list and the per-channel
// latest-post links are derived from the post pages already written to
// publicDir/feeds/posts, so they never go stale — unlike the rest of
// the page's hand-written copy, which lives only in this template, not
// read from anywhere at runtime. siteURL is the site's root (no
// trailing slash, no "/feeds"); pass "" to omit the canonical/Open
// Graph tags that need an absolute URL. categories are the extra merged
// category feeds to list under "RSS feeds".
func WriteHomepage(publicDir, siteURL string, channels, categories []string) error {
	postsDir := filepath.Join(publicDir, "feeds", "posts")

	sections := make([]homepageChannel, 0, len(channels))
	for _, ch := range sortedChannels(channels) {
		sections = append(sections, homepageChannel{Name: ch, Posts: latestPostsFrom(postsDir, ch, siteURL)})
	}

	data := homepageData{
		Description:     siteDescription,
		SiteURL:         strings.TrimRight(siteURL, "/"),
		Channels:        sortedChannels(channels),
		Categories:      sortedChannels(categories),
		ChannelSections: sections,
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

var (
	postTitleRe = regexp.MustCompile(`(?s)<title>\s*(.*?)\s*</title>`)
	postDateRe  = regexp.MustCompile(`&middot; (\d{4}-\d{2}-\d{2})`)
)

// latestPostsFrom lists the newest post pages on disk for one channel.
// Message IDs increase with time, so the highest numeric filenames are
// the newest posts. Title and date are pulled from each page's own
// <title> and meta line; when the title is missing, the date alone is
// used as the link text.
func latestPostsFrom(postsDir, channel, siteURL string) []homepagePost {
	dir := filepath.Join(postsDir, channel)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	ids := make([]int64, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		id, err := strconv.ParseInt(strings.TrimSuffix(e.Name(), ".html"), 10, 64)
		if err != nil {
			continue
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] > ids[j] })
	if len(ids) > HomepagePostsPerChannel {
		ids = ids[:HomepagePostsPerChannel]
	}

	posts := make([]homepagePost, 0, len(ids))
	for _, id := range ids {
		name := strconv.FormatInt(id, 10) + ".html"
		content, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}

		date := ""
		if m := postDateRe.FindSubmatch(content); m != nil {
			date = string(m[1])
		}
		title := ""
		if m := postTitleRe.FindSubmatch(content); m != nil {
			title = html.UnescapeString(strings.TrimSpace(string(m[1])))
		}
		if title == "" {
			title = date
		}
		if title == "" {
			title = "Post " + strconv.FormatInt(id, 10)
		}

		posts = append(posts, homepagePost{
			Date:  date,
			Title: title,
			URL:   postPageURL(siteURL+"/feeds", channel, id),
		})
	}
	return posts
}
