import eslintPluginAstro from "eslint-plugin-astro";

export default [
  ...eslintPluginAstro.configs.recommended,
  {
    ignores: ["dist/**", ".astro/**", ".vercel/**", "node_modules/**"],
  },
];
