# Firekeeper landing: recording TODO

One entry per media slot in `index.html`. Each slot is a `<figure id="…" data-slot>`; drop the finished files into `media/` as `ID.webm`, `ID.mp4`, `ID.jpg` (poster) and swap the placeholder `.label` for the `<video>` snippet in the comment at the top of `index.html`.

Sizes below are the **export** size: 2× the largest CSS width the slot renders at (full-width slots are 1052 px wide at 1440; the side-by-side cards are 655 px wide at tablet width, where they stack). Anything bigger wastes bytes.

## Status

- [x] `hero-demo`: **stand-in shipped** from the MacBook Air take (2112×1216, ~8.9 s, full Ghostty window). It shows Processes browsing and state changes only, with no `c` / chat / `Esc` / `s`. Reshoot the full story below (12–18 s) and replace the three files in `media/`.
- [x] `processes-live`: shipped from the same take, full window (2112×1216, ~8.9 s).
- [x] `switch-terminal`: shipped (1600×924, ~14.7 s). Full Ghostty window, no crop; longer than the 5–8 s target but loops cleanly.
- [x] `web-dashboard`: shipped (1600×1040, ~13.0 s). Full screen capture (Safari window on the desktop), no crop.
- [ ] Remaining placeholders: `chat-follow`, `usage-tab`, `backfill-dry-run`, `og-image`. Optional fuller `hero-demo` reshoot.

Shipped clips keep the full recorded window; each media slot's `--ar` matches the encoded size and the video uses `object-fit: contain`, so nothing inside the window is cropped. Only black desktop padding outside the window is trimmed.

## Before you record (applies to every shot)

**Machine and data**
- [ ] Use a throwaway macOS user or a clean `$HOME`, so paths read `/Users/demo/…` and not `/Users/danbradbury/…`. The current `firekeeper-tui.jpg` shows `/Users/danbradbury/Documents/Github/firekeeper` and a TTY. Fine for you, but not what you want on a sales page.
- [ ] Demo projects only: e.g. `~/code/aurora-api`, `~/code/aurora-web`, `~/code/notes`, with harmless branch names (`main`, `feat/billing`, `fix/pager`). Nothing client-related, and no real repo names you'd mind seeing.
- [ ] Real agents running real but boring prompts ("add a health check endpoint", "write tests for the pager"), so states, models and tokens are genuine. Never fake the UI.
- [ ] Before each take, check for API keys, tokens, emails, hostnames, Slack/Linear URLs in prompts or replies. `firekeeper config show` masks the token, but still keep it off screen.
- [ ] Hide the macOS menu bar clock or set it to something neutral; turn on Do Not Disturb; hide the dock or move it off screen.

**Terminal (Ghostty recommended; it's also one of the `s` switch targets)**
- Font: JetBrains Mono (or Geist Mono / Berkeley Mono), **15–16 pt**, line height 1.2–1.3. Bigger than you'd use day to day; it has to read when scaled down to ~300 px wide on phones.
- Theme: a dark theme that matches the site, warm-neutral background close to `#110f0d` and light text close to `#e7e0d8`. Ember `#f97316` already shows up in the TUI, so avoid themes with lots of competing oranges/reds. Use no transparency, blur or background image.
- Window: hide the title bar or use minimal chrome; the site no longer draws a fake OS window frame around videos (rounded card only), so title-bar chrome in the recording will show. Use `window-padding-x/y = 12` so text isn't flush to the edge.
- Columns × rows: about **160×50** for the 16:10 / 16:9 shots, and about **110×42** for the 4:3 cards. On Dan's MacBook Air the largest Ghostty window is **154×38**: use that for the wide shots and about **110×38** (or crop the sides) for the 4:3 cards. Size the window in pixels to the target aspect ratio (below) *before* launching `firekeeper`.
- Prompt: short, e.g. `PS1='%F{8}~/code/aurora-api%f $ '` (zsh). No git status segments, no host or user name.
- Cursor: a block that doesn't blink (blinking adds frames that change and makes files bigger).

**Retina / 2× capture**
- Record on a Retina display at native 2× (or set the external monitor to a 2× "looks like" mode). Capture at the physical pixel size, so a window that is 1056×660 points records at 2112×1320 px.
- Don't record at 1× and upscale; terminal text goes soft. Don't record at 3×+; it just gets downscaled.
- Tools: macOS `Cmd-Shift-5` → *Record Selected Portion* (fine), or CleanShot X / Screen Studio (set 60 fps, no cursor effects, no zoom, no background). Record at 60 fps and export at 30 fps.
- Record each shot as its own take of 2–3× the target length, then trim to a clean loop where the first and last frames match (same selection, same state).

**Motion and pacing**
- One idea per clip. Pause ~1 s on the key frame (the state change, the reply arriving).
- Show key presses as on-screen kbd hints only if your tool does it subtly, and pick one style for all clips. Otherwise the step captions on the page do that job.
- No mouse pointer in TUI shots. In browser shots, use a normal-size cursor and move it slowly.

## Shot list

### 1. `hero-demo`, the hero, top of page (most important)
- **Where:** directly under the hero CTA, full width of the rail.
- **Show, step by step:**
  1. The Processes view with 3–4 sessions (Claude Code, Codex, Copilot CLI, OpenCode) across two projects, mostly `WAITING`.
  2. One session flips to `ACTIVE`; tokens tick up.
  3. Arrow down to it and press `c`. The conversation opens and a reply streams in.
  4. Press `Esc` back to the list, then `s`. Cut to the agent's own Ghostty tab coming to the front (or end on the `s` hint if the cut looks bad).
  5. Back to the list to close the loop.
- **Format:** looping muted video, WebM (VP9) plus MP4 (H.264) fallback and a poster JPG. Not a GIF.
- **Aspect / size:** 16:10, export **2112×1320**.
- **Length:** 12–18 s.
- **Target size:** ≤ 2.5 MB WebM, ≤ 3.5 MB MP4, poster ≤ 150 KB.
- **Poster frame:** step 1 (the full list), since that's what people see before it plays or with reduced motion.
- **Optional `hero-demo-narrow`:** the same story recorded in a narrow window (about 80×48 cols, **4:5, 1080×1350**), served to phones under 560 px, where the 16:10 clip is only ~300 px wide. The existing `firekeeper-tui-narrow.jpg` is the right framing.

### 2. `processes-live`, "See what is running"
- **Where:** first feature section, full width under the copy.
- **Show:** four sessions, each grouped with its child processes (press `Enter` once to expand one). Over the clip, one session goes `WAITING → ACTIVE → NEEDS INPUT` so all three states in the legend appear. Let the 2-second refresh tick visibly, then move the selection up and down slowly.
- **Format:** looping WebM + MP4 + poster.
- **Aspect / size:** 16:9, **2112×1188**.
- **Length:** 8–12 s.
- **Target size:** ≤ 2 MB WebM.
- **Tip:** a Claude Code session waiting on a tool-approval prompt is the easiest way to get a real `NEEDS INPUT`.

### 3. `chat-follow`, "Read the reply" (left card)
- **Where:** Conversation section, left of the pair.
- **Show:** select a session, press `c`, and the conversation view opens. A new assistant message arrives live, then `Esc` back to the list.
- **Format:** looping WebM + MP4 + poster.
- **Aspect / size:** 4:3, **1320×990** (window about 110×42 cols).
- **Length:** 6–10 s.
- **Target size:** ≤ 1.2 MB WebM.
- **Watch for:** the reply text is fully readable here, so make sure the prompt and reply contain nothing private.

### 4. `switch-terminal`, "Jump to the terminal" (right card)
- **Where:** Conversation section, right of the pair.
- **Show:** a desktop with two or three Ghostty windows/tabs. Firekeeper is focused; press `s` on a session and that agent's tab comes to the front. This is a **desktop** capture, not a single window.
- **Format:** looping WebM + MP4 + poster.
- **Aspect / size:** 4:3, **1320×990**. Capture a 4:3 region of the screen, not the whole display.
- **Length:** 5–8 s.
- **Target size:** ≤ 1.5 MB WebM.
- **Setup:** use a plain solid wallpaper (near-black), with no other apps visible. macOS only. The caption says "macOS shown".

### 5. `usage-tab`, "Understand usage"
- **Where:** Usage section, full width.
- **Show:** press `Tab` from Processes to Usage, then step through providers. Show local token history by model and, separately, provider limits (Claude Code and Codex have both), plus a per-session token breakdown if it fits.
- **Format:** looping WebM + MP4 + poster.
- **Aspect / size:** 16:9, **2112×1188**.
- **Length:** 8–12 s.
- **Target size:** ≤ 2 MB WebM.
- **Watch for:** plan names and limits are your real account data. Decide whether you're OK showing them, or use an account you don't mind showing.

### 6. `backfill-dry-run`, reporting (left card)
- **Where:** "Stay local. Add a web dashboard…" section, left card.
- **Show:** plain shell. `firekeeper backfill --all --dry-run` prints its summary (nothing uploaded). Then `firekeeper login --server https://dashboard.firekeeper.ai` with the browser approval cut short, then `firekeeper daemon install`. If that's too much, the dry run alone is fine.
- **Format:** looping WebM + MP4 + poster. Alternatively an asciinema cast rendered with `agg`, but match the font and theme.
- **Aspect / size:** 4:3, **1320×990**.
- **Length:** 10–14 s. Type at a readable pace or paste whole commands; don't "hacker type".
- **Target size:** ≤ 1 MB WebM (mostly static text, so it compresses well).
- **Watch for:** the server URL, account email and machine name in `whoami`/`login` output. Use a demo account.

### 7. `web-dashboard`, the commercial dashboard (right card)
- **Where:** same section, right card. The frame draws a browser bar reading `dashboard.firekeeper.ai`.
- **Show:** the dashboard with a few machines and sessions from demo data. Click into one session and scroll a redacted transcript a little, so the redaction markers are visible.
- **Format:** looping WebM + MP4 + poster.
- **Aspect / size:** 4:3, **1320×990**. Set the browser viewport to 660×495 CSS px at 2×, or capture the content area only. No browser chrome; the page draws it.
- **Length:** 10–14 s.
- **Target size:** ≤ 1.5 MB WebM.
- **Watch for:** use a demo org and seeded data on a local `firekeeper serve` or a staging server, not your real dashboard. Hide the account menu and email.

### 8. `og-image`, social card (still, not on the page body)
- **Where:** `<meta property="og:image">` / `og.png`.
- **Show:** the hero poster frame with "Firekeeper: one terminal dashboard for your coding agents" set in the site's type on the dark background.
- **Format:** PNG or JPG, **1200×630**, ≤ 300 KB. The current `og.png` is 408 KB.

## Export recipes (ffmpeg)

```sh
IN=raw/hero-demo.mov; ID=hero-demo; W=2112; H=1320   # set per shot

# 1) Trim + scale + 30 fps master (lossless-ish intermediate)
ffmpeg -ss 00:00:01.2 -to 00:00:16.8 -i "$IN" \
  -vf "fps=30,scale=${W}:${H}:flags=lanczos,format=yuv420p" -an -c:v libx264 -crf 12 -preset slow tmp-$ID.mp4

# 2) WebM (VP9), primary source. Two-pass, about 1.2–2.5 MB for 15 s of terminal
ffmpeg -i tmp-$ID.mp4 -c:v libvpx-vp9 -b:v 0 -crf 36 -row-mt 1 -deadline good -cpu-used 2 -pass 1 -an -f null /dev/null && \
ffmpeg -i tmp-$ID.mp4 -c:v libvpx-vp9 -b:v 0 -crf 36 -row-mt 1 -deadline good -cpu-used 2 -pass 2 -an media/$ID.webm

# 3) MP4 (H.264), Safari / fallback. faststart so it plays before fully loaded
ffmpeg -i tmp-$ID.mp4 -c:v libx264 -crf 24 -preset veryslow -tune animation -pix_fmt yuv420p \
  -movflags +faststart -an media/$ID.mp4

# 4) Poster frame (pick the timestamp of your key frame)
ffmpeg -ss 0.5 -i tmp-$ID.mp4 -frames:v 1 -q:v 3 media/$ID.jpg

# If a clip is over budget: raise -crf by 2–4, or drop to 24 fps (fps=24). Terminal text tolerates both.
```

Embed (already templated in `index.html`):

```html
<video autoplay muted loop playsinline preload="metadata" poster="media/hero-demo.jpg" width="2112" height="1320">
  <source src="media/hero-demo.webm" type="video/webm">
  <source src="media/hero-demo.mp4" type="video/mp4">
</video>
```

**Why not GIF:** a 15 s 2× terminal GIF is typically 10–30 MB with banding, versus 1–3 MB for WebM/MP4 at better quality. Use GIF only where video can't play, such as the GitHub README. If you need one for the README:

```sh
ffmpeg -i tmp-$ID.mp4 -vf "fps=15,scale=1000:-1:flags=lanczos,split[a][b];[a]palettegen=max_colors=96[p];[b][p]paletteuse=dither=bayer:bayer_scale=4" readme-$ID.gif
```
(Or `gifski --fps 15 --width 1000`, which gives better quality.)

## Page-side checks after dropping media in
- [ ] Total media on first load ≤ ~4 MB. Hero is eager; give the other clips `preload="none"` and lazy-start them with a tiny IntersectionObserver if needed.
- [ ] `prefers-reduced-motion`: show the poster only (remove `autoplay` via a 3-line script, or add a Pause button like noctty).
- [ ] Every `<video>` keeps its `width`/`height` attributes so there's no layout shift. The slots already reserve the aspect ratio.
- [ ] Add an `aria-label` or visually hidden text per clip describing what it shows. Captions are already under each frame.
- [ ] Check light mode. The window frames stay dark on purpose, like a real terminal.

## Manual post-sellout checklist (first 100 Personal licenses)

Do this by hand when the $29 launch Payment Link sells out or you decide to end the launch offer. There is **no** automatic sellout detection in the repo, and this checklist does not claim Stripe's quantity/cap was verified.

- [ ] In Stripe, confirm the $29 launch link is exhausted / disabled (or you are ending the offer).
- [ ] In `docs/landing/_redirects`, point `/buy/personal` at the $49 Personal Payment Link (the live $49 URL is already noted in the PR that landed the redirects; keep the $29 URL commented or in a PR comment for history).
- [ ] On this landing page (`index.html`):
  - [ ] **Remove the hero launch offer** — the eyebrow under the hero that says "Launch pricing · Personal license $29 for the first 100 buyers".
  - [ ] **Replace the actual $29 price** in the Personal tier: drop the "Launch price · first 100" tag, remove the struck `$49` / `$29` pair, and show **`$49` one-time** as the live price.
  - [ ] Keep "Then $29 a year for updates" (or update that line if renewal pricing changes).
- [ ] Spot-check live firekeeper.ai: `/buy/personal` 302s to the $49 Stripe Checkout, hero no longer advertises $29, Personal card shows $49.
