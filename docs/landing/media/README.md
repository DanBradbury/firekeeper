# Drop finished recordings here as ID.webm / ID.mp4 / ID.jpg
# See ../RECORDING-TODO.md

## Shipped

| ID | Size | WebM | MP4 | Poster | Source |
| --- | --- | --- | --- | --- | --- |
| `hero-demo` | 2112×1216, ~8.9 s | 515 KB | 416 KB | 201 KB | MacBook Air take, black desktop padding trimmed, full Ghostty window |
| `processes-live` | 2112×1216, ~8.9 s | 515 KB | 416 KB | 201 KB | Same take as hero, full window |
| `switch-terminal` | 1600×924, ~14.7 s | 684 KB | 463 KB | 187 KB | Ghostty window capture, uncropped |
| `web-dashboard` | 1600×1040, ~13.0 s | 653 KB | 860 KB | 155 KB | Full screen capture with Safari, uncropped |

`hero-demo` is a **stand-in**: the take only shows Processes browsing and state
changes (no `c` chat, `Esc`, or `s` switch). Replace it when a fuller hero
reshoot exists. The take was recorded on Dan's MacBook Air, where the largest
Ghostty window is **154×38**.

Encodes keep the full recorded window (2026-11-06): only black desktop padding
outside the window is trimmed, never the TUI or browser content. Each slot's
`--ar` and the `<video>` width/height match the encoded size, and the CSS uses
`object-fit: contain`, so the clip fills its card without cutting anything off.
- `usage-tab` — 1600×906, contain, full window (no pad crop)
