# Firekeeper documentation

Firekeeper is a free, open-source terminal dashboard for coding agents
running on your machine. Install it, start your agents as usual, and run
`firekeeper` in another terminal. No Firekeeper account or server is needed.

**New here? [Install and find your first session](https://firekeeper.ai/docs/quick-start/).**
For everyday use, start with these guides:

- [Quick start](https://firekeeper.ai/docs/quick-start/)
- [How to work with Firekeeper](https://firekeeper.ai/docs/how-to-work/)
- [Understand usage](https://firekeeper.ai/docs/usage/)
- [Keyboard shortcuts](https://firekeeper.ai/docs/keyboard/)
- [Troubleshooting](https://firekeeper.ai/docs/troubleshooting/)

## Reference

- [Install and uninstall](install.md), including manual verification and source builds
- [Full reference](reference.md): providers, dashboard, reporting, and configuration
- [HTTP API](api.md) and [event schema](event.schema.json)
- [Privacy](../PRIVACY.md)

## Editing and previewing the website

The usage guides live in [landing/docs/](landing/docs/). Each guide is a
static `index.html` with shared styles in `docs.css`. No JavaScript or build
step is required. Keep the sidebar and previous/next links consistent when
adding a page, and add its URL to [landing/sitemap.xml](landing/sitemap.xml).
The Markdown references above remain the detailed technical documentation;
link to them rather than duplicating the full command and configuration reference.

Serve `docs/landing` as the site root so absolute links and assets resolve.
For example, from the repository root with Python 3 installed:

```sh
python3 -m http.server 8000 --bind 127.0.0.1 --directory docs/landing
```

Open `http://localhost:8000/docs/`. Stop the preview with Ctrl+C.
The existing [Cloudflare Pages workflow](../.github/workflows/landing.yml)
publishes this directory on changes merged to `main`; no separate docs
deployment is needed.
