import rss from "@astrojs/rss";
import { getCollection } from "astro:content";
import { site } from "../config/site";
import type { APIContext } from "astro";

export async function GET(context: APIContext) {
  const posts = await getCollection("blog", ({ data }) => !data.draft);
  return rss({
    title: site.name,
    description: site.description,
    site: context.site ?? site.url,
    items: posts
      .sort((a, b) => b.data.date.valueOf() - a.data.date.valueOf())
      .map((post) => ({
        title: post.data.title,
        description: post.data.description,
        pubDate: post.data.date,
        link: `/blog/${post.id}/`,
      })),
  });
}
