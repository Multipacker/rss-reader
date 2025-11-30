let feeds = new Map();
let entries = [];

// NOTE(simon): Load and unpack feeds and entries, then update the results list.
fetch("feeds")
    .then(response => response.json())
    .then(rawFeeds => {
        rawFeeds.forEach(rawFeed => {
            const feed = {
                title:       rawFeed.title,
                description: rawFeed.description,
                link:        rawFeed.link,
                updated:     new Date(rawFeed.updated),
            };

            feeds.set(rawFeed.id, feed);
        });
    });
fetch("entries")
    .then(response => response.json())
    .then(rawEntries => {
        entries = rawEntries.flatMap(rawEntry => {
            const entry = { ...rawEntry };
            entry.published = new Date(entry.published);
            entry.update    = new Date(entry.update);
            return entry;
        });
    })
    .then(() => update_list());

const read_articles = new Set(JSON.parse(localStorage.getItem("read_articles")));

const save_read = (id) => {
    read_articles.add(id);
    localStorage.setItem("read_articles", JSON.stringify([...read_articles.values()]));

    // NOTE(simon): A bit wasteful to recalculate all list items, but this way
    // the link state will be correct when returning from the article.
    update_list();
};

// NOTE(simon): Assumes that the text to be highlighted is interleaved between
// non-highlighted elements. That is, items at odd indicies should be
// highlighted.
const highlight = (parts) => parts.map((value, index) => {
    if (index % 2 == 1) {
        const mark = document.createElement("mark");
        mark.textContent = value;
        return mark;
    } else {
        return document.createTextNode(value);
    }
});

const update_list = () => {
    // NOTE(simon): Acquire DOM elements.
    const search_type = document.getElementById("search_type");
    const search = document.getElementById("search");
    const result_list = document.getElementById("results");

    // NOTE(simon): Construct queries.
    const search_terms = search.value
        .split(/\s+/)
        .filter(term => term.length !== 0)
    const search_regex = new RegExp(
        "(" +
        search_terms
            .map(term => `(?:${RegExp.escape(term)})`)
            .join("|") +
        ")",
        "i"
    );

    const template = document.getElementById("template");
    switch (search_type.value) {
        case "Articles": {
            result_list.replaceChildren(
                ...entries
                .map(item => {
                    let new_item = {...item};

                    const feed = feeds.get(item.feed);
                    new_item.author = feed.title ?? "";

                    if (search_terms.length !== 0) {
                        new_item.title  = new_item.title.split(search_regex);
                        new_item.author = new_item.author.split(search_regex);
                        new_item.title_matches  = Math.floor((new_item.title.length - 1) / 2);
                        new_item.author_matches = Math.floor((new_item.author.length - 1) / 2);
                    } else {
                        new_item.title  = [new_item.title];
                        new_item.author = [new_item.author];
                        new_item.title_matches  = 0;
                        new_item.author_matches = 0;
                    }

                    return new_item;
                })
                .filter(item => {
                    return item.title_matches >= search_terms.length || item.author_matches >= search_terms.length;
                })
                .sort((a, b) => {
                    // NOTE(simon): More title matches should appear earlier.
                    if (a.title_matches !== b.title_matches) {
                        return b.title_matches - a.title_matches;
                    }

                    // NOTE(simon): More author matches should appear earlier.
                    if (a.author_matches !== b.author_matches) {
                        return b.author_matches - a.author_matches;
                    }

                    // NOTE(simon): Newer dates should appear earlier.
                    return b.published - a.published;
                })
                .map(item => {
                    const elem = document.importNode(template.content, true);

                    const htmlItem        = elem.querySelector(".item");
                    const itemLink        = elem.querySelector(".item-link");
                    const itemDescription = elem.querySelector(".item-description");

                    if (read_articles.has(item.id)) {
                        htmlItem.classList.add("read");
                    }
                    itemLink.append(...highlight(item.title));
                    itemLink.setAttribute("href", item.link);
                    itemLink.onclick = () => save_read(item.id);
                    itemDescription.append(...highlight(item.author), document.createTextNode(` ${item.published.toLocaleString()}`));

                    return elem;
                })
            );
        } break;
        case "Feeds": {
            result_list.replaceChildren(
                ...feeds
                .values()
                .map(feed => {
                    let new_feed = {...feed};

                    if (search_terms.length !== 0) {
                        new_feed.title       = new_feed.title.split(search_regex);
                        new_feed.description = new_feed.description.split(search_regex);
                        new_feed.title_matches       = Math.floor((new_feed.title.length - 1) / 2);
                        new_feed.description_matches = Math.floor((new_feed.description.length - 1) / 2);
                    } else {
                        new_feed.title       = [new_feed.title];
                        new_feed.description = [new_feed.description];
                        new_feed.title_matches       = 0;
                        new_feed.description_matches = 0;
                    }

                    return new_feed;
                })
                .filter(feed => {
                    return feed.title_matches >= search_terms.length || feed.description_matches >= search_terms.length;
                })
                .toArray()
                .sort((a, b) => {
                    // NOTE(simon): More title matches should appear earlier.
                    if (a.title_matches !== b.title_matches) {
                        return b.title_matches - a.title_matches;
                    }

                    // NOTE(simon): More description matches should appear earlier.
                    if (a.description_matches !== b.description_matches) {
                        return b.description_matches - a.description_matches;
                    }

                    // NOTE(simon): Newer dates should appear earlier.
                    return b.updated - a.updated
                })
                .map(feed => {
                    const elem = document.importNode(template.content, true);

                    const itemLink        = elem.querySelector(".item-link");
                    const itemDescription = elem.querySelector(".item-description");

                    itemLink.append(...highlight(feed.title));
                    itemLink.setAttribute("href", feed.link);
                    itemDescription.append(...highlight(feed.description));

                    return elem;
                })
            );
        } break;
    }
};
