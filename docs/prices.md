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
label = "Claude Sonnet 4.6"      # optional: the row label on the page, if not name
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
as a name or alias) or label. The top-level `as_of` must equal the oldest entry's
`as_of`, so the date shown on the page is never newer than any price behind
it.

### What goes in, and what stays out

The weekly refresh applies these rules itself (see
[New models](#new-models)); they also apply to hand edits.

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
  TOML comment directly above the entry, with no blank line between. Leave a
  blank line after a comment about a whole section, because the refresh
  removes an entry together with the comment lines directly above it.
- Keep each `input`, `output`, `cache` and `as_of` on its own line as
  `key = value`; the refresh rewrites those lines in place.
- The source must be a page the refresh can read (see the table below), and
  `label` must be the row's exact text on it, footnote markers aside.

### Pricing pages

| Provider | Page | What the refresh reads |
| --- | --- | --- |
| Anthropic | <https://platform.claude.com/docs/en/about-claude/pricing> | The first table under "Model pricing", rows labelled by display name (so entries need `label`): Base input tokens, Output tokens, Cache hits and refreshes. Model IDs: <https://platform.claude.com/docs/en/about-claude/models/overview>. |
| OpenAI | <https://developers.openai.com/api/docs/pricing> | Every Standard-tier table, rows labelled by model ID: Input, Cached input, Output, short-context columns when there are both. Batch, Flex, Fast and untiered tables are skipped. |
| Moonshot AI (Kimi) | <https://platform.kimi.ai/docs/pricing/chat> | Every price table, rows labelled by model ID: Input (cache miss), Output, Input (cache hit), with the unit checked to be "1M tokens". |

Each provider also serves the page as Markdown at the same URL plus `.md`,
which is what the refresh reads.

GitHub Copilot bills through plans and credits, not per token, so it has no
table of its own. Copilot sessions are priced by the underlying model's list
price where the name matches, which estimates the API cost of the same tokens,
not what Copilot charged.

## Keeping the table current

The table maintains itself: rates, retirements and new models. The **Refresh
default prices** workflow (`.github/workflows/prices-refresh.yml`) runs every
Monday, and on demand from the Actions tab:

1. `go run ./internal/server/prices/cmd/refresh-prices` reads the Markdown
   version of every source page and looks up each entry's row by its `label`,
   or its `name` when there is no label. It never fetches anything at server
   run time.
2. If every rate still matches and the table is under 28 days old, nothing
   happens. If every rate matches and the table is older, every `as_of` moves
   to today. If rates changed, they and every `as_of` are rewritten. Only
   those lines change; comments, labels and aliases stay as they are.
3. An entry whose row is gone from its page, or no longer reads as a price, is
   **removed**, following the rule that a price that cannot be confirmed is
   left out.
4. A model the pages list that the table does not is **added** (see below).
5. A changed table goes to a pull request from the `prices/refresh` branch,
   with the changes and sources in its description. Pull requests opened with
   the workflow's token do not start CI on their own, so the workflow
   dispatches CI on that branch, waits for it, and **squash-merges** the pull
   request when it passes.

### New models

A row on a pricing page becomes a new entry only when all of these hold:

- Its input, cached-input and output prices all read as dollar amounts. A
  model without a cached-input price, such as OpenAI's `-pro` models, or
  marked "Free", stays out.
- Its model ID comes from a provider page. For OpenAI and Kimi that is the
  row's own label, less any note in parentheses such as
  `(<272K context length)`, which is kept as the entry's `label`. Anthropic's
  pricing table names models by display name, so the ID and alias come from
  the "Claude API ID" and "Claude API alias" rows of the models overview
  (<https://platform.claude.com/docs/en/about-claude/models/overview>), which
  lists the current lineup. A model missing there, such as a legacy one, is
  not added. IDs are never derived from a name.
- Every row naming the model gives the same price. A model priced in tiers,
  one row per prompt length (Claude Haiku 5.5), stays out.
- The ID is lowercase letters, digits, `.`, `-` and `_`, is not already a name
  or alias in the table, and is not in the table's `exclude` list.
- On OpenAI's page, the row is in a Standard-tier table with token price
  columns only. Tables with another qualifying column, such as fine-tuning
  ("Training") or image models ("Modality"), are skipped, so a fine-tuned
  rate never becomes a model's base price.

The pull request lists every added model and, in a collapsed section, every
row that was not added and why. To keep a model out for good, add its ID to
`exclude` at the top of the table.

Aliases come only from a provider page (Anthropic's "Claude API alias").
Names that only a harness reports, such as Copilot CLI's
`claude-sonnet-4.6`, are not derived automatically; `firekeeper serve prices
missing` shows them, and they are added to `aliases` by hand.

### Guardrails

The refresh never merges a guess:

- **It fails, changing nothing,** when a page cannot be fetched, a page's
  layout is no longer recognized (a section, column or unit it relies on is
  gone), a source has no parser, or more than half of one page's entries
  have no readable row at once, which looks like a renaming rather than
  retirements. The workflow then opens an "Automatic price refresh failed"
  issue, or comments on the open one, with the log.
- **It opens the pull request but does not merge it** when any rate moves by
  more than 50%, changes to or from zero, or exceeds $1,000 per million
  tokens; when a new model has a zero or implausibly high rate; when more
  than 5 models are added at once, which suggests a parser read the wrong
  table; or when it removed an entry (a removal could also be a renamed
  row). The pull request says why, for a person to check against the pages.
- Every rewritten table must pass the same validation as a hand edit, and CI
  must pass before anything merges.

Separately, the workflow opens a "Default price table is out of date" issue
once the top-level `as_of` is more than 45 days old, which only happens when
refreshes keep failing or a pull request waits too long for review. Check
locally with `scripts/prices-reminder.sh --dry-run`.

### What still needs a person

- **Layout changes.** When a provider redesigns its page, update its parser in
  `internal/server/prices/refresh/pages.go` and the synthetic page in
  `internal/server/prices/refresh/testdata` to match.
- **Pull requests a guardrail held back,** and promotional prices noted in
  comments.
- **Harness-specific aliases,** such as Copilot CLI's dotted Claude names.
- **A new provider,** which needs a parser in `pages.go` and a first entry.

To run a refresh locally without changing anything:

```sh
cp internal/server/prices/defaults.toml /tmp/prices.toml
go run ./internal/server/prices/cmd/refresh-prices -table /tmp/prices.toml
```
