# PgQueryNarrative website

The public marketing site: landing page, demo page, blog, and get-started guide.
This is a separate, static Astro project. It does not build, run, or depend on
the Go application, `frontend/`, or `web/` in this repository, and it never
connects to a database or calls the product's API.

## Local development

```
cd website
npm ci
npm run dev
```

Open `http://localhost:4321`.

## Production build

```
npm run build     # outputs to website/dist
npm run preview   # serve the built output locally
```

`npm run check` runs `astro check` (type checking). `npm run lint` runs ESLint.

## Branding assets

Logo and mark SVGs (light/dark variants) live in `public/brand/`. The `Logo`
component (`src/components/Logo.astro`) picks the correct variant based on the
active theme. Do not edit these files by hand; they are the approved brand
assets copied from `docs/assets/`.

## Blog

Posts are Markdown files in `src/content/blog/`. Each post needs frontmatter:

```yaml
title: "..."
description: "..."
date: 2026-01-01
author: "PgQueryNarrative"   # optional, defaults to this
image: "..."                  # optional
tags: ["..."]                 # optional
draft: true                   # omit or set false to publish
```

A post with `draft: true` is excluded from the production build (`/blog`,
the RSS feed, and sitemap) but still renders when running `npm run dev`.

## Demo video

Set `demoVideoId` (and `demoVideoUrl`) in `src/config/site.ts` once the demo
video is recorded and uploaded. Until then, `DemoVideo.astro` renders an
explicit "coming soon" placeholder instead of a broken embed.

## Deployment (Vercel)

This project deploys independently of the rest of the repository.

- Root Directory: `website`
- Framework Preset: Astro
- Install Command: `npm ci`
- Build Command: `npm run build`
- Output Directory: `dist`

No `vercel.json` is checked in; the Astro + Vercel zero-config integration
(`@astrojs/vercel` in `astro.config.mjs`) covers this without one.

Do not set any database, LLM, or session-secret environment variables on this
Vercel project. The site is static and never needs them.

## Git flow

Feature branch → pull request → Vercel Preview deployment on the PR → review
→ merge to `main` → Vercel Production deployment.
