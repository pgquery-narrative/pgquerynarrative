/// <reference types="vite/client" />

interface ImportMetaEnv {
  /** Opt in to storing the API key in browser localStorage in production builds. Default: dev-only. */
  readonly VITE_ALLOW_BROWSER_API_KEY?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
