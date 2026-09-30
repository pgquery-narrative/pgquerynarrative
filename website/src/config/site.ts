// Central site configuration. Keep every external URL here instead of
// scattering literals across components and pages.

export const site = {
  name: "PgQueryNarrative",
  tagline: "Investigate slow PostgreSQL queries with evidence.",
  description:
    "PgQueryNarrative reads a PostgreSQL execution plan, proposes a bounded rewrite or index from the query's own parse tree, compares before and after, verifies the results match, and writes the evidence into a report.",
  // Not finalized. Change this once the domain is live; everything else
  // (canonical URLs, OG images, sitemap, RSS) derives from it.
  url: "https://pgquerynarrative.com",

  githubUrl: "https://github.com/pgquery-narrative/pgquerynarrative",
  issuesUrl: "https://github.com/pgquery-narrative/pgquerynarrative/issues",
  releasesUrl: "https://github.com/pgquery-narrative/pgquerynarrative/releases",
  licenseUrl: "https://github.com/pgquery-narrative/pgquerynarrative/blob/main/LICENSE",
  docsUrl: "https://pgquery-narrative.github.io/pgquerynarrative/",
  quickstartUrl: "https://pgquery-narrative.github.io/pgquerynarrative/getting-started/quickstart/",
  installUrl: "https://pgquery-narrative.github.io/pgquerynarrative/getting-started/installation/",

  // TODO(demo-video): no video has been uploaded yet. This is a placeholder
  // YouTube ID, not a real one. Replace with the real video ID once the
  // demo is recorded and uploaded, and DemoVideo.astro will pick it up
  // everywhere it's used without further code changes.
  demoVideoId: "",
  demoVideoUrl: "",

  twitterHandle: "",
} as const;

export type Site = typeof site;
