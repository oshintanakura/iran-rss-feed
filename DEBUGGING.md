# Debugging

Notes for tracking down why a specific post is missing, stuck, or wrong,
without guessing.

## Inspect the state database directly

`state.db` is a plain SQLite3 file — the standard `sqlite3` CLI works on
it fine, regardless of which driver wrote it.

```sh
sqlite3 state.db
```

Useful queries:

```sql
-- overall counts
SELECT
  COUNT(*) AS total,
  SUM(translated IS NOT NULL AND translated != 'SKIPPED_TOO_LONG') AS translated,
  SUM(translated IS NULL) AS pending_or_failed,
  SUM(translated = 'SKIPPED_TOO_LONG') AS skipped_too_long
FROM posts;

-- posts that failed translation and should retry on the next run
SELECT channel, message_id, url FROM posts WHERE translated IS NULL;

-- one specific post
SELECT * FROM posts WHERE channel = 'CHANNEL' AND message_id = ID;

-- sanity check: no two rows should ever share a URL (PRIMARY KEY should
-- make this structurally impossible; if this ever returns rows, something
-- is seriously wrong)
SELECT url, COUNT(*) c FROM posts GROUP BY url HAVING c > 1;

-- most recently touched rows
SELECT channel, message_id, translated_at FROM posts ORDER BY translated_at DESC LIMIT 20;
```

## Force a post (or a whole channel) to retranslate

Delete its row(s) and the next run treats it as new:

```sql
DELETE FROM posts WHERE channel = 'CHANNEL' AND message_id = ID;
-- or, for the whole channel:
DELETE FROM posts WHERE channel = 'CHANNEL';
```

## Run one channel locally without touching the real API quota

```sh
TRANSLATE_API_KEY=... TRANSLATE_API_BASE_URL=... TRANSLATE_API_MODEL=... \
  ./tgfeed --config config.yaml --channel CHANNEL --dry-run
```

Logs exactly what would be sent (post count, char estimate) and makes
zero chat API calls.

## Read the actual production logs

Every run's structured logs (per-channel `fetched/new/translated/failed`,
plus any `level=ERROR`/`level=WARN` lines) live in the Actions run log:

```sh
gh run list --workflow update-feeds --limit 5
gh run view <run-id> --log
```

Grep for `level=ERROR`, `level=WARN`, or `run summary` to skip straight to
the interesting lines.

## Spot-check what's actually live

- Combined feed: `<base_url>/all.xml`
- One channel: `<base_url>/<channel>.xml`
- A single translated post's permanent page:
  `<base_url>/posts/<channel>/<message_id>.html`

## Known non-bugs (don't re-diagnose these from scratch)

- `selector returned 0 posts for <channel>` can mean the channel has
  disabled Telegram's web preview, not a real markup break — see
  README's "A channel isn't producing any posts" before assuming the
  scraper broke.
- A post missing from a feed days after being fetched is usually one of:
  skipped as `SKIPPED_TOO_LONG` (check the query above), still
  `translated IS NULL` and due to retry next run, or older than
  `runtime.max_post_age_days` at fetch time (dropped before ever being
  considered).
- If a post is stuck with `translated IS NULL` across *many* runs
  without ever clearing, that's the bug class fixed once already (see
  git history: `FilterUnseen` must treat `translated IS NULL` as unseen,
  not just a hash mismatch) — check that fix hasn't regressed before
  assuming it's a new issue.
