# Landing fonts

Self-hosted under `docs/landing/assets/fonts/` so Cloudflare Pages ships them with the site (no CDN, no build step).

## Binaries (reproducible)

| File | Source | Version pin | Raw download | sha256 |
| --- | --- | --- | --- | --- |
| `Geist-Variable.woff2` | npm `geist` (Vercel) | `1.3.1` (`gitHead` `e444db839c07d5de21b6a7b4dbab507e40cc5bb8`) | https://cdn.jsdelivr.net/npm/geist@1.3.1/dist/fonts/geist-sans/Geist-Variable.woff2 | `28258d0621216948416a859d32487ab6ad1c9effa0d08795698e70be3c917630` |
| `JetBrainsMono-Variable.woff2` | npm `@fontsource-variable/jetbrains-mono` (latin, normal axis) | `5.3.0` | https://cdn.jsdelivr.net/npm/@fontsource-variable/jetbrains-mono@5.3.0/files/jetbrains-mono-latin-wght-normal.woff2 | `18be452724bfdc236c074ca94a249a7f41a86752c7d04ab258ce9ed5651f6a7e` |

Re-fetch:

```sh
curl -fsSL -o Geist-Variable.woff2 \
  'https://cdn.jsdelivr.net/npm/geist@1.3.1/dist/fonts/geist-sans/Geist-Variable.woff2'
curl -fsSL -o JetBrainsMono-Variable.woff2 \
  'https://cdn.jsdelivr.net/npm/@fontsource-variable/jetbrains-mono@5.3.0/files/jetbrains-mono-latin-wght-normal.woff2'
sha256sum Geist-Variable.woff2 JetBrainsMono-Variable.woff2
```

## License notices (required with every redistribute)

SIL Open Font License 1.1 full text + upstream copyright, byte-identical to upstream:

- `Geist-OFL.txt` — from https://raw.githubusercontent.com/vercel/geist-font/main/LICENSE.txt (also https://github.com/vercel/geist-font/blob/main/LICENSE.txt)
- `JetBrainsMono-OFL.txt` — from https://raw.githubusercontent.com/JetBrains/JetBrainsMono/master/OFL.txt (also https://github.com/JetBrains/JetBrainsMono/blob/master/OFL.txt)

Upstream project homes: https://github.com/vercel/geist-font and https://github.com/JetBrains/JetBrainsMono.

Do not replace these files with a license name or URL alone. OFL condition 2 requires the copyright notice and the full license text to accompany every copy.
