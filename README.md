# iran-rss-feed

A single Go binary that polls public Persian-language Telegram channels every
30 minutes, translates new posts to English with an OpenAI-compatible chat
completions API, and writes Atom feed XML to a directory. It does not serve
HTTP — point a static host (GitHub Pages, Cloudflare Pages, nginx) at the
output directory.

Running it twice in a row costs nothing extra: posts already translated are
never re-sent to the chat API, and feeds are simply regenerated from what's
already stored.

## Getting a chat completions API key

Any OpenAI-compatible chat completions provider works — pick one, get an API
key, its base URL, and the model name you want to use.

1. In your GitHub repo, go to **Settings → Secrets and variables → Actions**
   and add three secrets: `TRANSLATE_API_KEY`, `TRANSLATE_API_BASE_URL`
   (e.g. `https://api.your-provider.example.com`), and `TRANSLATE_API_MODEL`
   (the model name your provider expects).
2. The workflow in [.github/workflows/update.yml](.github/workflows/update.yml)
   passes all three to the binary as environment variables, and `config.yaml`
   picks them up via `${TRANSLATE_API_KEY}` / `${TRANSLATE_API_BASE_URL}` /
   `${TRANSLATE_API_MODEL}`. All three are required — missing any of them is
   a fatal startup error, before any fetching happens.

Locally, export the same three variables before running:

```sh
export TRANSLATE_API_KEY=...
export TRANSLATE_API_BASE_URL=https://api.your-provider.example.com
export TRANSLATE_API_MODEL=...
go build -o tgfeed ./cmd/tgfeed
./tgfeed --config config.yaml
```

## Adding channels

Copy [config.example.yaml](config.example.yaml) to `config.yaml` and edit the
`channels` list:

```yaml
channels:                 # plain list of usernames, no @ and no t.me/
  - varzesh3
  - khabar_fori
```

To stop polling a channel, delete its line — there's no per-channel enable
flag or display title; the feed's `<author>` and the visible "Source:"
line in each item both just use the plain username.

## Web mode needs no Telegram account

With the default `source.mode: web`, the program scrapes the public
`https://t.me/s/<username>` preview page. **No Telegram account, login, or
channel join is required** — this works for any public channel and is what
CI should use, since ephemeral runners logging into a real account from a
new IP every run is a pattern Telegram flags.

### A channel isn't producing any posts

The web scraper hits `https://t.me/s/<username>`, which is a preview page
a channel admin can disable independently of the channel itself being
public. If that's off, the URL just redirects to the plain `t.me/<username>`
page instead of showing posts, and the program logs `selector returned 0
posts for <channel> — page markup may have changed` — same message as an
actual markup change, but the fix here is different: check
`https://t.me/s/<that-channel>` in a browser yourself; if it redirects
away instead of showing messages, `mode: web` cannot reach that channel no
matter what, and the only way to fetch it is `mode: mtproto` (see below).
A few channels in `config.yaml` are marked this way and kept in the list
in case that ever changes on their end.

## MTProto mode (optional)

`source.mode: mtproto` logs in as a real Telegram user via the MTProto API,
which is more resilient than scraping HTML but requires a one-time
interactive login. Use this only on a stable, persistent machine — not on
GitHub-hosted CI runners.

1. Go to [my.telegram.org](https://my.telegram.org), log in, open **API
   development tools**, and create an app to get `api_id` and `api_hash`.
2. Set them in `config.yaml` under `source.telegram` (directly or via
   `${TG_API_ID}` / `${TG_API_HASH}` environment variables).
3. Run the binary once, interactively, with `source.mode: mtproto`. It will
   prompt on stdin for your phone number, then the login code Telegram sends
   you (and a 2FA password if you have one). On success it writes the
   session to `source.telegram.session_file` (default `./tg.session`).
4. On every later run the session file is reused and you are never prompted
   again. Keep that file private — it's equivalent to being logged into your
   account. If you run this mode from CI, copy the session file there
   ahead of time rather than trying to log in non-interactively; the
   program fails fast with an explanatory message if the file is missing
   and no terminal is attached.

## Serving the feeds (GitHub Pages)

The program writes `<username>.xml` per channel plus `all.xml` (if
`combined_feed: true`) into `output.dir` (default `./public/feeds`), plus
one standalone HTML page per translated post at
`<output.dir>/posts/<channel>/<message_id>.html` — this is what each feed
item's `<link>` points to, so clicking a post opens the translation
itself instead of the original Telegram channel. Post pages are never
pruned; they stay as a permanent archive even after a post ages out of
the feed's `max_feed_age_days` window.

[.github/workflows/update.yml](.github/workflows/update.yml) uploads the
`public` directory as a Pages artifact and deploys it with
`actions/deploy-pages` after every run — repo **Settings → Pages → Source**
must be "GitHub Actions" (already set). `public/index.html` is a static
homepage (not written by the program) with a short description and a
disclaimer that the listed channels haven't endorsed or agreed to this in
any way, plus a link to the combined feed.

Feed URLs are `https://<your-host>/feeds/<username>.xml` and
`https://<your-host>/feeds/all.xml`, matching `output.base_url` in the
config.

Not using GitHub Pages? Point any static host's build output at `public`
instead — e.g. **Cloudflare Pages**: set the build output directory to
`public` and disable the build command (nothing to build — the workflow
commits the generated files directly).

## Scheduling

[.github/workflows/update.yml](.github/workflows/update.yml) runs the binary
on a `0 */6 * * *` cron (every 6 hours) and commits `state.db` plus
`public/feeds` back to the repo. GitHub Actions' scheduled workflows drift
under load, so a run can land somewhat later than exactly 6 hours out —
that's fine for an RSS feed; nothing here assumes a precise interval.

## Resetting a single channel

To force a channel's posts to be re-translated (e.g. after a translation
quality fix), delete its rows from the sqlite state file and let the next
run repopulate them:

```sh
sqlite3 state.db "DELETE FROM posts WHERE channel = 'varzesh3';"
```

The next run will treat every post from that channel as new and re-fetch,
re-translate, and re-save it.

## Other runtime knobs worth knowing about

- `runtime.max_post_age_days` (default 7) — posts older than this are
  dropped before translation, on every run, not just the first one for a
  newly-added channel. Keeps a rarely-posting or just-added channel from
  ever backfilling months-old content.
- `output.max_feed_age_days` (default 10) — every translated post newer
  than this stays in its feed (and in the combined `all.xml`), no matter
  how many that ends up being. `output.max_items_per_feed` (default
  2000) is a hard ceiling on top of that, only there to stop a
  pathological runaway — at any realistic posting volume it never
  actually trims the age window.
- `translate.max_chars_per_post` (default 4096) — matches Telegram's own
  hard limit on a single message's length, so it's a safety net rather
  than something you'd expect to actually trigger; posts over it are
  skipped (not truncated) and never retried.
- Pure media posts (a photo or voice note with no caption) are always
  skipped — there's no text to translate. A photo *with* a caption is
  still translated normally.

## CLI flags

- `--config PATH` — path to the YAML config (default `./config.yaml`)
- `--once` — run one cycle and exit; this is the default and only mode
- `--dry-run` — fetch and log what would be translated, call the chat API zero
  times, write zero files
- `--channel NAME` — restrict fetching/translating to one channel, for
  debugging (feeds for all other channels are still regenerated from
  whatever is already stored)

## Architecture

```
cmd/tgfeed/main.go      — flag parsing, config load, wiring, run one cycle, exit
internal/config         — struct + loader + ${ENV} expansion + validation
internal/source         — Source interface + webSource + mtprotoSource
internal/store          — sqlite: schema, seen-check, insert, query for feed build
internal/translate      — chat API batching client
internal/feed           — Atom XML generation
```

See [DEBUGGING.md](DEBUGGING.md) for how to inspect `state.db`, force a
retranslation, and tell a real bug apart from expected behavior when a
post looks missing or wrong.
