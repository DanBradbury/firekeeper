// Renders og.html into docs/landing/og.png and docs/landing/apple-touch-icon.png.
//   NODE_PATH=$(npm root -g) node docs/landing-og/render.js
// Needs Playwright with a Chromium build.
const path = require('path');
const { chromium } = require('playwright');

(async () => {
  const out = path.join(__dirname, '..', 'landing');
  const opts = process.env.PLAYWRIGHT_CHROMIUM ? { executablePath: process.env.PLAYWRIGHT_CHROMIUM } : {};
  const browser = await chromium.launch(opts);
  const page = await browser.newPage({ viewport: { width: 1200, height: 900 }, deviceScaleFactor: 1 });
  await page.goto('file://' + path.join(__dirname, 'og.html'));
  await page.waitForLoadState('networkidle');
  await page.locator('#og').screenshot({ path: path.join(out, 'og.png') });
  await page.locator('#icon').screenshot({ path: path.join(out, 'apple-touch-icon.png') });
  await browser.close();
})();
