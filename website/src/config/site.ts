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
  packagesUrl: "https://github.com/pgquery-narrative?tab=packages",
  licenseUrl: "https://github.com/pgquery-narrative/pgquerynarrative/blob/main/LICENSE",
  docsUrl: "https://pgquery-narrative.github.io/pgquerynarrative/",
  quickstartUrl: "https://pgquery-narrative.github.io/pgquerynarrative/getting-started/quickstart/",
  installUrl: "https://pgquery-narrative.github.io/pgquerynarrative/getting-started/installation/",

  demoVideoId: "enNpmCH0nvc",
  demoVideoUrl: "https://www.youtube.com/watch?v=enNpmCH0nvc",

  // Update on each tagged release (no build-time GitHub API call, kept simple
  // for a static site). Source of truth: `gh release list`.
  latestRelease: "v3.0.1",
  latestReleaseUrl: "https://github.com/pgquery-narrative/pgquerynarrative/releases/tag/v3.0.1",

  twitterHandle: "",
} as const;

export type Site = typeof site;
