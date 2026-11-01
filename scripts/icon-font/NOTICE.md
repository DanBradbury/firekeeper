# Icon font notice

`svg/*.svg` are monochrome logo marks taken from
[`@lobehub/icons-static-svg`](https://github.com/lobehub/lobe-icons)
(MIT licensed package). The marks themselves are trademarks of Anthropic,
OpenAI, GitHub, Anysphere (Cursor), Google, Moonshot AI (Kimi) and the OpenCode
project. They are used here only to identify those tools in Firekeeper's
dashboard, and are not covered by Firekeeper's license.

The built font is committed at `internal/iconfont/FirekeeperIcons.ttf` and
embedded in the `firekeeper` binary; `firekeeper install-font` installs it on
request. `npm run build` here rebuilds it.

Codepoints (private use area, U+E9A0 to U+E9A6) are listed in `build.mjs` and
must match `toolFontIcons` in `internal/tui/icons.go`.
