// Package feed renders stored items into Atom XML files, plus one
// standalone HTML page per translated post.
package feed

import (
	"fmt"
	"html"
	"html/template"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/feeds"

	"iran-rss-feed/internal/store"
)

// Options controls how items are rendered.
type Options struct {
	BaseURL         string // used to build the feed's own <link>
	IncludeOriginal bool   // append the Persian original inside each item
}

// feedURL builds the public URL for the feed file named <name>.xml.
func feedURL(baseURL, name string) string {
	return strings.TrimRight(baseURL, "/") + "/" + name + ".xml"
}

// postPageURL builds the public URL for one post's standalone page.
func postPageURL(baseURL, channel string, messageID int64) string {
	return strings.TrimRight(baseURL, "/") + "/posts/" + channel + "/" + strconv.FormatInt(messageID, 10) + ".html"
}

// Write renders items into an Atom XML file at dir/name.xml, atomically
// (write to a temp file, then rename) so a reader never sees a
// half-written file.
func Write(dir, name, title string, items []store.Item, opts Options) error {
	feed := &feeds.Feed{
		Title:   title,
		Link:    &feeds.Link{Href: feedURL(opts.BaseURL, name)},
		Created: time.Now().UTC(),
	}

	for _, it := range items {
		feed.Items = append(feed.Items, itemFor(it, opts))
	}

	atom, err := feed.ToAtom()
	if err != nil {
		return fmt.Errorf("rendering atom for %s: %w", name, err)
	}

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating output dir %s: %w", dir, err)
	}

	final := filepath.Join(dir, name+".xml")
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, []byte(atom), 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, final); err != nil {
		return fmt.Errorf("renaming %s to %s: %w", tmp, final, err)
	}
	return nil
}

func itemFor(it store.Item, opts Options) *feeds.Item {
	return &feeds.Item{
		Title: titleFor(it.Translated, it.MessageID),
		// Link points at this post's own standalone page, not the
		// Telegram post — readers should stay on the translated text
		// instead of jumping to Telegram. Id still pins to the original
		// t.me URL regardless of Link, and must stay stable forever for
		// dedup.
		Link:        &feeds.Link{Href: postPageURL(opts.BaseURL, it.Channel, it.MessageID)},
		Id:          it.URL,
		Created:     it.PostedAt,
		Author:      &feeds.Author{Name: it.Channel},
		Description: bodyFor(it, opts),
	}
}

// titleFor takes the first 80 characters on a word boundary, appending …
// if truncated. Falls back to "Post <id>" if the translation is empty.
func titleFor(translated string, messageID int64) string {
	text := strings.TrimSpace(translated)
	if text == "" {
		return fmt.Sprintf("Post %d", messageID)
	}
	if len(text) <= 80 {
		return text
	}
	cut := text[:80]
	if idx := strings.LastIndexAny(cut, " \t\n"); idx > 0 {
		cut = cut[:idx]
	}
	return strings.TrimSpace(cut) + "…"
}

// bodyFor renders the feed item's body: a "Source: @channel" line, the
// translated text, and (if enabled) the Persian original.
func bodyFor(it store.Item, opts Options) string {
	var b strings.Builder
	b.WriteString(`<p><strong>Source:</strong> @`)
	b.WriteString(html.EscapeString(it.Channel))
	b.WriteString(`</p>`)
	b.WriteString(translatedContent(it, opts))
	return b.String()
}

// translatedContent renders just the translated text (plus the original,
// if enabled) — no source line, since the standalone post page shows
// that in its own header instead.
func translatedContent(it store.Item, opts Options) string {
	var b strings.Builder
	b.WriteString(toHTML(it.Translated))
	if opts.IncludeOriginal {
		b.WriteString(`<hr><details><summary>Original (فارسی)</summary><div dir="rtl">`)
		b.WriteString(toHTML(it.Original))
		b.WriteString(`</div></details>`)
	}
	return b.String()
}

// toHTML HTML-escapes text, then converts newlines to <br> so paragraph
// breaks survive in feed readers.
func toHTML(text string) string {
	escaped := html.EscapeString(text)
	return strings.ReplaceAll(escaped, "\n", "<br>")
}

var postPageTmpl = template.Must(template.New("post").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
  body { font-family: system-ui, -apple-system, sans-serif; max-width: 700px; margin: 2rem auto; padding: 0 1rem; line-height: 1.6; color: #1a1a1a; }
  .meta { color: #666; font-size: 0.9rem; margin-bottom: 1.5rem; }
  a { color: #0645ad; }
</style>
</head>
<body>
<p class="meta">Source: <strong>@{{.Channel}}</strong> &middot; {{.PostedAt}}</p>
<div>{{.Body}}</div>
</body>
</html>
`))

type postPageData struct {
	Title    string
	Channel  string
	PostedAt string
	Body     template.HTML
}

// WritePostPages renders one standalone HTML page per translated post in
// items, atomically (temp file + rename). Pages are never pruned — old
// ones just stay as a permanent archive, since they're plain text and
// cheap to keep.
func WritePostPages(dir string, items []store.Item, opts Options) error {
	for _, it := range items {
		data := postPageData{
			Title:    titleFor(it.Translated, it.MessageID),
			Channel:  it.Channel,
			PostedAt: it.PostedAt.UTC().Format("2006-01-02 15:04 UTC"),
			Body:     template.HTML(translatedContent(it, opts)), //nolint:gosec // content is HTML-escaped by translatedContent before this point
		}

		channelDir := filepath.Join(dir, "posts", it.Channel)
		if err := os.MkdirAll(channelDir, 0o755); err != nil {
			return fmt.Errorf("creating posts dir %s: %w", channelDir, err)
		}

		final := filepath.Join(channelDir, strconv.FormatInt(it.MessageID, 10)+".html")
		tmp := final + ".tmp"
		f, err := os.Create(tmp)
		if err != nil {
			return fmt.Errorf("creating %s: %w", tmp, err)
		}
		if err := postPageTmpl.Execute(f, data); err != nil {
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
