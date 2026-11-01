# Icon font notice

`svg/*.svg` are monochrome logo marks copied unchanged from
[`@lobehub/icons-static-svg`](https://github.com/lobehub/lobe-icons) version
1.101.0 (MIT licensed, Copyright (c) 2023 LobeHub; `copilot.svg` is that
package's `githubcopilot.svg`). The marks themselves are trademarks of Anthropic,
OpenAI, GitHub, Anysphere (Cursor), Google, Moonshot AI (Kimi) and the OpenCode
project. They are used here only to identify those tools in Firekeeper's
dashboard, and are not covered by Firekeeper's license.

The built font is committed at `internal/iconfont/FirekeeperIcons.ttf` and
embedded in the `firekeeper` binary; `firekeeper install-font` installs it on
request. `npm run build` here rebuilds it.

Codepoints (private use area, U+E9A0 to U+E9A6) are listed in `build.mjs` and
must match `toolFontIcons` in `internal/tui/icons.go`.

## License of the SVG files

The built font, and so every `firekeeper` binary, contains these marks, so
release archives carry this license text in `THIRD_PARTY_NOTICES.md`
(`scripts/third-party-notices.sh` copies the block below). Update the version
above, and the block below if it changed, when the SVGs are replaced.

```text
MIT License

Copyright (c) 2023 LobeHub

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.
```
