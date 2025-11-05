// NOTE(simon): Info taken from here:
// * https://www.rssboard.org/rss-specification#ltguidgtSubelementOfLtitemgt
// * https://www.ietf.org/rfc/rfc4287.txt

// TODO(simon):
// * Respect TTL, currently we only allow using the global fetch interval.
// * Use ETAGs to not fetch old feeds.
//   * https://developer.mozilla.org/en-US/docs/Web/HTTP/Reference/Headers/ETag
//   * https://xeiaso.net/blog/site-update-rss-bandwidth-2021-01-14/
// * Save inline copies of the articles to allow showing them in our own reader.

import fs from "node:fs/promises";

import { XMLParser, XMLBuilder, XMLValidator} from "fast-xml-parser";
import sanitizeHtml from "sanitize-html";

// NOTE(simon): Parse settings
const rawConfig = await fs.readFile("config.json", { encoding: "utf8", }).catch(() => "[]");
const config = JSON.parse(rawConfig);

// NOTE(simon): Set up default settings.
config.urls     = config.urls     ?? "[]";
config.output   = config.output   ?? "feeds.json";
config.interval = config.interval ?? 24;

// NOTE(simon): Load saved feeds.
const feeds = new Map();
{
    const feedBlob = await fs.readFile(config.output, { encoding: "utf8", }).catch(() => "[]");
    const rawFeedList = JSON.parse(feedBlob);
    for (const rawFeed of rawFeedList) {
        const articles = new Map();
        for (const rawArticle of rawFeed.articles) {
            const article = {
                title:      rawArticle.title,
                link:       rawArticle.link,
                id:         rawArticle.id,
                published:  new Date(rawArticle.published),
                updated:    new Date(rawArticle.published),
            };
            articles.set(article.id, article);
        }

        const feed = {
            title:       rawFeed.title,
            description: rawFeed.description,
            link:        rawFeed.link,
            id:          rawFeed.id,
            articles:    articles,
            updated:     new Date(rawFeed.updated),
        };
        feeds.set(feed.id, feed);
    }
}

const updateFeeds = async () => {
    console.group(`Update feeds (${(new Date()).toLocaleString("sv-SE", { timeZoneName: "longOffset", })})`);

    // NOTE(simon): Download feeds.
    console.time("Downloading feeds");
    const rawFeeds = await Promise.all(
        config.urls.map(url => fetch(url)
            .then(response => {
                if (!response.ok) {
                    throw new Error(response.status);
                }
                return response.text();
            })
            .then(text => ({ url: url, text: text, }))
            .catch(error => {
                console.error(`Failed to download feed from '${url}': ${error.message}`);
                return "";
            })
        )
    );
    console.timeEnd("Downloading feeds");

    // NOTE(simon): Parse and remap feeds to internal format.
    console.time("Parsing to internal structure");
    const xmlParser = new XMLParser({
        ignoreAttributes: false,
        alwaysCreateTextNode: true,
        isArray: (name, path, isLeafNode, isAttribute) => {
            const arrayPaths = [
                // NOTE(simon): RSS arrays
                "rss.channel.item",
                // NOTE(simon): Atom arrays
                "feed.entry",
                "feed.entry.link",
            ];

            return arrayPaths.indexOf(path) !== -1;
        },
    });
    const newFeeds = rawFeeds
        .filter(({ url, text, }) => text && text.length !== 0)
        .map(({ url, text, }) => {
            // NOTE(simon): Fill out default values.
            const feed = {
                title:       undefined,
                description: undefined,
                link:        undefined,
                id:          undefined,
                articles:    [],
                updated:     new Date(),
            };

            try {
                let xmlFeed = xmlParser.parse(text, true);

                if (xmlFeed.rss) {
                    const rss = xmlFeed.rss.channel;

                    if (rss.title) {
                        feed.title = sanitizeHtml(rss.title["#text"] ?? "");
                    }

                    if (rss.description) {
                        feed.description = sanitizeHtml(rss.description["#text"] ?? "");
                    }

                    if (rss.link && rss.link["#text"]) {
                        feed.link = feed.id = (new URL(rss.link["#text"])).href;
                    } else {
                        feed.link = feed.id = url;
                    }

                    feed.articles = rss.item.map(item => {
                        // NOTE(simon): Fill out default values.
                        const article = {
                            title:      undefined,
                            link:       undefined,
                            id:         undefined,
                            published:  new Date(),
                            updated:    new Date(),
                        };

                        // NOTE(simon): Must be defined.
                        if (item.title) {
                            article.title = sanitizeHtml(item.title["#text"] ?? "");
                        }

                        // NOTE(simon): The link must exist, but in case it
                        // doesn't, use the guid instead.
                        if (item.link) {
                            article.link = (new URL(item.link["#text"])).href;
                        } else if (item.guid) {
                            article.link = (new URL(item.guid["#text"])).href;
                        }

                        // NOTE(simon): The guid is optional, so use the link if it
                        // doesn't exist.
                        if (item.guid) {
                            article.id = item.guid["#text"];
                        } else if (item.link) {
                            article.id = item.link["#text"];
                        }

                        if (item.pubDate && item.pubDate["#text"]) {
                            article.published = article.updated = new Date(item.pubDate["#text"]);
                        }

                        return article;
                    });

                    if (rss.lastBuildDate) {
                        feed.updated = new Date(rss.lastBuildDate["#text"]);
                    } else if (rss.pubDate) {
                        feed.updated = new Date(rss.pubDate["#text"]);
                    }
                } else if (xmlFeed.feed) {
                    const atom = xmlFeed.feed;

                    if (atom.title) {
                        feed.title = sanitizeHtml(atom.title["#text"] ?? "");
                    }

                    // NOTE(simon): At most one
                    if (atom.subtitle) {
                        feed.description = sanitizeHtml(atom.subtitle["#text"] ?? "");
                    }

                    // NOTE(simon): Should
                    if (atom.link) {
                        feed.link = (new URL(atom.link.find(link => link["@_rel"] === "self")["@_href"] ?? url)).href;
                    } else {
                        feed.link = url;
                    }

                    // NOTE(simon): Must
                    if (atom.id) {
                        feed.id = atom.id["#text"];
                    }

                    feed.articles = atom.entry.map(entry => {
                        // NOTE(simon): Fill out default values.
                        const article = {
                            title:     undefined,
                            link:      undefined,
                            id:        undefined,
                            published: new Date(),
                            updated:   new Date(),
                        };

                        if (entry.title) {
                            article.title = sanitizeHtml(entry.title["#text"]);
                        }

                        // NOTE(simon): Grab the link. MUST
                        if (Array.isArray(entry.link)) {
                            // NOTE(simon): Try to use the one labeled "alternate" and default to the first one.
                            const link_node = entry.link.find(link => link["@_rel"] === "alternate") ?? entry.link[0];
                            if (link_node) {
                                article.link = (new URL(link_node["@_href"] ?? link_node["#text"])).href;
                            }
                        }

                        if (entry.id) {
                            article.id = entry.id["#text"];
                        }

                        if (entry.published) {
                            article.published = new Date(entry.published["#text"]);
                        } else if (entry.updated) {
                            article.published = new Date(entry.updated["#text"]);
                        }

                        if (entry.published) {
                            article.updated = new Date(entry.updated["#text"]);
                        }

                        return article;
                    });

                    if (atom.updated && atom.updated["#text"]) {
                        feed.updated = new Date(atom.updated["#text"]);
                    }
                }
            } catch (error) {
                console.error(`Failed to parse feed of '${url}': ${error.message}`);
            }

            return feed;
        });
    console.timeEnd("Parsing to internal structure");

    // NOTE(simon): Append new articles and feeds.
    console.time("Updating store");
    for (const newFeed of newFeeds) {
        let feed = feeds.get(newFeed.id);
        if (!feed) {
            feed = {
                title:       newFeed.title,
                description: newFeed.description,
                link:        newFeed.link,
                id:          newFeed.id,
                articles:    new Map(),
                updated:     newFeed.updated,
            };

            feeds.set(newFeed.id, feed);
        }

        // NOTE(simon): Store articles that either didn't exist or have been
        // updated.
        for (const newArticle of newFeed.articles) {
            const article = feed.articles.get(newArticle.id);
            if (!article || newArticle.updated > article.updated) {
                feed.articles.set(newArticle.id, newArticle);
            }
        }
    }
    console.timeEnd("Updating store");

    // NOTE(simon): Serialize to disk.
    console.time("Serializing");
    const flattened = [
        ...feeds.values().map(feed => ({
            title:       feed.title,
            description: feed.description,
            link:        feed.link,
            id:          feed.id,
            articles:    [...feed.articles.values()],
            updated:     feed.updated,
        }))
    ];
    fs.writeFile(config.output, JSON.stringify(flattened));
    console.timeEnd("Serializing");

    console.groupEnd();
};

await updateFeeds();
setInterval(updateFeeds, config.interval * 60 * 60 * 1000);
