# Prices and estimated cost

The dashboard's Usage page and `GET /v1/usage` show an **estimated cost** for
the tokens your agents used. This page covers where the prices come from, how
to override or turn them off, how models are matched, and how maintainers keep
the built-in table current.

## What the estimate is, and is not

Firekeeper multiplies each model's stored token counts by a price per million
tokens:

```
cost = (input × input rate + output × output rate + cache × cache rate) / 1,000,000
```

The built-in rates are the providers' **published list prices** in USD, as of
the date shown next to the cost. The result is not what any subscription,
seat, plan or invoice actually charged. In particular:

- Subscriptions (ChatGPT, Claude, Copilot and others) bill differently from
  per-token API prices, often not per token at all.
- Cache writes, long-context tiers, batch discounts, regional or priority
  pricing and taxes are not modelled. `cache` is one counter: for Claude it
  holds cache reads and cache writes together, and both are priced at the
  cache-read rate, so the estimate is low for heavy cache writing. Models with
  a long-context tier use their short-context rate.
- Token counts come from transcripts, which may be incomplete.

The server never fetches prices from the network. The table is compiled into
the binary.

## Overriding prices and turning the defaults off

Set prices in the config file that `firekeeper serve` reads
(`~/.firekeeper/config.toml`, or the file `FIREKEEPER_CONFIG` names):

```toml
[prices]
defaults = true          # false turns the built-in table off completely

[prices."gpt-5"]         # replaces the built-in price for gpt-5 only
input = 1.0              # USD per million tokens
output = 8.0
cache = 0.1

[prices."my-local-model"]  # adds a model the built-in table does not list
input = 0.5
output = 1.5
cache = 0
```

Rules:

- Your price for a model always wins over the built-in one, for that model
  only. Every other model keeps its built-in price.
- If the built-in table lists aliases for a model you priced, the aliases use
  your price too, unless you price an alias by name.
- `defaults = false` removes every built-in price. With no prices of your own
  either, the Usage page shows tokens only, as it did before built-in prices
  existed.
- `defaults` is reserved: it cannot be used as a model name.
- Rates must not be negative. A typo in a key, such as `ouput`, is an error.

`firekeeper config show` prints `defaults = false` when it is set, and your own
price tables.

The API reports which prices a cost used in `price_source` (`default`,
`custom` or `mixed`) and the built-in table's date in `prices_as_of`; see
[api.md](api.md#get-v1usage).

## How models are matched

Transcripts often carry dated or suffixed model names. A model gets a price in
this order, and no other way:

1. Its **exact name** in your `[prices]` table or the built-in table.
2. An **alias** listed on a built-in entry (for example, Copilot CLI reports
   `claude-sonnet-4.6` for the model Anthropic calls `claude-sonnet-4-6`).
3. Otherwise **no price**. Its tokens are counted in `unpriced_tokens`, and
   the page marks the cost as partial.

There is no prefix, case-insensitive or fuzzy matching: a wrong price is worse
than no price. To price a name the table does not know, add it to your
`[prices]` table.

## Finding unpriced models

```sh
firekeeper serve prices missing [--db PATH]
```

prints every model name in the dashboard database that has no price, with its
input, output, cache and total token counts, across all accounts. It reads the
same config file as `serve`, so your own prices and `defaults` apply. It
prints names and counts only, never transcript text. Names are taken from
transcripts, so one with unprintable characters is shown quoted.

## The built-in table

The table is `internal/server/prices/defaults.toml`, embedded with `go:embed`:

```toml
as_of = "2026-10-14"            # the oldest entry's as_of

[[model]]
name = "claude-sonnet-4-6"      # the provider's model ID
aliases = ["claude-sonnet-4.6"] # optional: other exact names harnesses report
input = 3                       # USD per million tokens
output = 15
cache = 0.30                    # cache-read (cached input) rate
source = "https://platform.claude.com/docs/en/about-claude/pricing"
as_of = "2026-10-14"            # the day the rates were read from source
```

`TestDefaultsValid` and `TestValidate` in `internal/server/prices` fail the
build when an entry lacks a `source` (an https URL) or an `as_of` date, has a
negative or non-finite rate, repeats a model name, or has a malformed alias
(empty, containing a space or control character, `defaults`, or already used
as a name or alias). The top-level `as_of` must equal the oldest entry's
`as_of`, so the date shown on the page is never newer than any price behind
it.

### What goes in, and what stays out

- Every rate comes from the provider's own public pricing page, recorded in
  `source` with the date in `as_of`. **If a figure cannot be confirmed from a
  public provider page, leave the model out.** Never fill rates in from
  memory or a third-party list. A third-party dataset may be used as a
  cross-check only, after checking its licence; say what you found in the
  pull request.
- A model ID must also appear on a provider page. Leave out names that only a
  harness reports, unless they are an alias of a listed model.
- Add an alias only for an exact name a harness is known to report, such as
  one seen in a fixture. Never add an alias to cover a guess.
- Leave out models whose price cannot be expressed as one input, output and
  cache rate, such as a price that depends on prompt length at a threshold
  agents routinely cross (Claude Haiku 5.5, above 100,000 tokens), or models
  with no cached-input price.
- Note anything unusual, such as promotional pricing with an end date, in a
  TOML comment next to the entry.

### Pricing pages

| Provider | Page | Rate columns |
| --- | --- | --- |
| Anthropic | <https://platform.claude.com/docs/en/about-claude/pricing> | Base input tokens, Output tokens, Cache hits and refreshes. Model IDs: <https://platform.claude.com/docs/en/about-claude/models/overview>. |
| OpenAI | <https://developers.openai.com/api/docs/pricing> | Standard tier: Input, Output, Cached input. Short-context rates. |
| Moonshot AI (Kimi) | <https://platform.kimi.ai/docs/pricing/chat> | Input (cache miss), Output, Input (cache hit). |

GitHub Copilot bills through plans and credits, not per token, so it has no
table of its own. Copilot sessions are priced by the underlying model's list
price where the name matches, which estimates the API cost of the same tokens,
not what Copilot charged.

## Refreshing the table

A scheduled workflow, `.github/workflows/prices-reminder.yml`, runs weekly and
opens a "Refresh the default price table" issue, or comments on the open one,
once the top-level `as_of` is more than 45 days old. It never fails a build.
Run `scripts/prices-reminder.sh --dry-run` to check locally.

To refresh:

1. Open each pricing page above. For every entry, compare the model ID and
   the three rates with the page.
2. Update rates that changed. Set each entry's `as_of` to today's date when
   you confirmed it, whether or not it changed, and keep `source` pointing at
   the page you read.
3. Remove entries for models the page no longer lists. Add new models only
   under the rules above. Check promotional-pricing comments.
4. Set the top-level `as_of` to the oldest entry's `as_of`.
5. Run `go test ./internal/server/prices/`.
6. Review the diff: every changed rate should match the page, and no entry
   should have changed without its `as_of` changing too. Paste the pages and
   dates you used in the pull request, and close the reminder issue.
