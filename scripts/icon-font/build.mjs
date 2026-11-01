// Builds dist/FirekeeperIcons.ttf from svg/*.svg. Each glyph sits at the
// codepoint internal/tui/icons.go expects; keep the two lists in step.
import { generateFonts } from "fantasticon";
import { fileURLToPath } from "node:url";
import path from "node:path";
import { copyFileSync, mkdirSync } from "node:fs";

const here = path.dirname(fileURLToPath(import.meta.url));

// U+E9A0.. sits in the gap between the Nerd Fonts ranges (they use e700-e8ef
// and ea60-ec1e), and the TrueType cmap built here cannot reach past the BMP.
const codepoints = {
  claude: 0xe9a0,
  codex: 0xe9a1,
  copilot: 0xe9a2,
  cursor: 0xe9a3,
  gemini: 0xe9a4,
  kimi: 0xe9a5,
  opencode: 0xe9a6,
};

mkdirSync(path.join(here, "dist"), { recursive: true });
await generateFonts({
  inputDir: path.join(here, "svg"),
  outputDir: path.join(here, "dist"),
  name: "FirekeeperIcons",
  fontTypes: ["ttf"],
  assetTypes: [],
  codepoints,
  normalize: true,
  fontHeight: 1000,
  descent: 150,
});
// The binary embeds this copy; `firekeeper install-font` installs it.
copyFileSync(
  path.join(here, "dist", "FirekeeperIcons.ttf"),
  path.join(here, "..", "..", "internal", "iconfont", "FirekeeperIcons.ttf"),
);
console.log("wrote dist/FirekeeperIcons.ttf and internal/iconfont/FirekeeperIcons.ttf");
