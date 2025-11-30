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
    const search      = document.getElementById("search");
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

    // NOTE(simon): Fetch the item template.
    const template = document.getElementById("template");

    // NOTE(simon): Collect diplay information.
    let displayItems = [];
    switch (search_type.value) {
        case "Articles": {
            displayItems = entries.map(item => {
                return {
                    title:       item.title,
                    description: `${feeds.get(item.feed).title} ${item.published.toLocaleString()}`,
                    link:        item.link,
                    date:        item.published,
                    id:          item.id,
                    readable:    true,
                };
            });
        } break;
        case "Feeds": {
            displayItems = feeds
                .values()
                .toArray()
                .map(feed => {
                    return {
                        title:       feed.title,
                        description: feed.description,
                        link:        feed.link,
                        date:        feed.published,
                        id:          feed.id,
                        readable:    false,
                    };
                });
        } break;
    }

    result_list.replaceChildren(
        ...displayItems
        .map(item => {
            if (search_terms.length !== 0) {
                item.title       = item.title.split(search_regex);
                item.description = item.description.split(search_regex);
                item.title_matches       = Math.floor((item.title.length - 1) / 2);
                item.description_matches = Math.floor((item.description.length - 1) / 2);
            } else {
                item.title       = [item.title];
                item.description = [item.description];
                item.title_matches       = 0;
                item.description_matches = 0;
            }
            return item;
        })
        .filter(item => {
            return item.title_matches >= search_terms.length || item.description_matches >= search_terms.length;
        })
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
            return b.date - a.date;
        })
        .map(item => {
            const elem = document.importNode(template.content, true);

            const htmlItem        = elem.querySelector(".item");
            const itemLink        = elem.querySelector(".item-link");
            const itemDescription = elem.querySelector(".item-description");

            if (item.readable) {
                if (read_articles.has(item.id)) {
                    htmlItem.classList.add("read");
                }
                itemLink.onclick = () => save_read(item.id);
            }

            itemLink.append(...highlight(item.title));
            itemLink.setAttribute("href", item.link);
            itemDescription.append(...highlight(item.description));

            return elem;
        })
    )
};
