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

var channelPageTmpl = template.Must(template.New("channel").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>@{{.Name}} — Iran Telegram Digest</title>
<meta name="description" content="Machine-translated English posts from the Telegram channel @{{.Name}}.">
{{if .SiteURL}}<link rel="canonical" href="{{.SiteURL}}/{{.Name}}.html">
<meta property="og:type" content="website">
<meta property="og:title" content="@{{.Name}} — Iran Telegram Digest">
<meta property="og:url" content="{{.SiteURL}}/{{.Name}}.html">
<meta name="twitter:card" content="summary">
{{end}}<link rel="alternate" type="application/atom+xml" title="@{{.Name}} RSS feed" href="feeds/{{.Name}}.xml">
<style>
  :root {
    color-scheme: light dark;
    --bg: #ffffff;
    --text: #1f2328;
    --muted: #5b6470;
    --link: #0a58ca;
  }
  @media (prefers-color-scheme: dark) {
    :root {
      --bg: #0d1117;
      --text: #e6edf3;
      --muted: #9aa4b2;
      --link: #6cb6ff;
    }
  }
  * { box-sizing: border-box; }
  body {
    background: var(--bg);
    color: var(--text);
    font-family: -apple-system, system-ui, "Segoe UI", Roboto, sans-serif;
    max-width: 700px;
    margin: 3rem auto;
    padding: 0 1.5rem;
    line-height: 1.7;
    font-size: 1.05rem;
  }
  h1 { font-size: 1.5rem; margin-bottom: 1rem; }
  a { color: var(--link); }
  .feed-link {
    display: inline-block;
    margin: 0.5rem 0.5rem 0.5rem 0;
    padding: 0.5rem 1rem;
    border: 1px solid var(--link);
    border-radius: 6px;
    text-decoration: none;
    font-weight: 600;
  }
  ol.posts { padding-left: 1.5rem; }
  ol.posts li { margin-bottom: 0.3rem; }
  .post-date { color: var(--muted); font-size: 0.85rem; margin-right: 0.5rem; }
  .none { color: var(--muted); }
</style>
</head>
<body>
<h1>@{{.Name}}</h1>

<p><a class="feed-link" href="feeds/{{.Name}}.xml">RSS feed</a>
<a class="feed-link" href="./">Home</a></p>

{{if .Posts}}
<ol class="posts">
{{range .Posts}}  <li><span class="post-date">{{.Date}}</span><a href="{{.URL}}">{{.Title}}</a></li>
{{end}}</ol>
{{else}}
<p class="none">No posts yet.</p>
{{end}}
</body>
</html>
`))

type channelPageData struct {
	SiteURL string
	Name    string
	Posts   []channelPost
}

type channelPost struct {
	Date  string
	Title string
	URL   string
}

var (
	postTitleRe = regexp.MustCompile(`(?s)<title>\s*(.*?)\s*</title>`)
	postDateRe  = regexp.MustCompile(`&middot; (\d{4}-\d{2}-\d{2})`)
)

// WriteChannelPages renders one page per channel at
// publicDir/<name>.html: an RSS feed button up top and a numbered list
// of that channel's posts, newest first, linking to the standalone post
// pages. Like the post pages, these are derived from the files already
// on disk, so they never go stale.
func WriteChannelPages(publicDir, siteURL string, channels []string) error {
	postsDir := filepath.Join(publicDir, "feeds", "posts")
	trimmed := strings.TrimRight(siteURL, "/")

	if err := os.MkdirAll(publicDir, 0o755); err != nil {
		return fmt.Errorf("creating public dir %s: %w", publicDir, err)
	}

	for _, ch := range channels {
		data := channelPageData{SiteURL: trimmed, Name: ch, Posts: channelPostsFrom(postsDir, ch, siteURL)}

		final := filepath.Join(publicDir, ch+".html")
		tmp := final + ".tmp"
		f, err := os.Create(tmp)
		if err != nil {
			return fmt.Errorf("creating %s: %w", tmp, err)
		}
		if err := channelPageTmpl.Execute(f, data); err != nil {
			f.Close()
			return fmt.Errorf("rendering %s: %w", final, err)
		}
		if err := f.Close(); err != nil {
			return fmt.Errorf("closing %s: %w", tmp, err)
		}
		if err := os.Rename(tmp, final); err != nil {
			return fmt.Errorf("renaming %s to %s: %w", tmp, final, err)
		}
	}
	return nil
}

// channelPostsFrom lists every post page on disk for one channel,
// newest first. Message IDs increase with time, so the highest numeric
// filenames are the newest posts. Title and date are pulled from each
// page's own <title> and meta line; when the title is missing, the
// date alone is used as the link text.
func channelPostsFrom(postsDir, channel, siteURL string) []channelPost {
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

	posts := make([]channelPost, 0, len(ids))
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

		posts = append(posts, channelPost{
			Date:  date,
			Title: title,
			URL:   postPageURL(siteURL+"/feeds", channel, id),
		})
	}
	return posts
}
