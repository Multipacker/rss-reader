let feeds = [];
let articles = [];

// NOTE(simon): Load and unpack feeds, then update the results list.
fetch("feeds.json")
    .then(response => response.json())
    .then(rawFeeds => {
        feeds = rawFeeds.map(rawFeed => {
            const feed = { ...rawFeed };
            feed.update = new Date(feed.updated);
            return feed;
        });

        articles = rawFeeds.flatMap(feed => feed
            .articles.map(article => ({
                author:    feed.title,
                title:     article.title,
                link:      article.link,
                id:        article.id,
                published: new Date(article.published),
                updated:   new Date(article.updated),
            }))
        );
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
        search_terms
            .map(term => `(${RegExp.escape(term)})`)
            .join("|"),
        "ig"
    );

    switch (search_type.value) {
        case "Articles": {
            result_list.replaceChildren(
                ...articles
                .map(item => {
                    let new_item = {...item};

                    new_item.title_matches = 0;
                    new_item.author_matches = 0;
                    if (search_terms.length !== 0) {
                        new_item.title = new_item.title.replace(search_regex, match => { ++new_item.title_matches; return `<mark>${match}</mark>`; });
                        new_item.author = new_item.author.replace(search_regex, match => { ++new_item.author_matches; return `<mark>${match}</mark>`; });
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
                    const elem = document.createElement("div")
                    elem.classList.add("item");
                    if (read_articles.has(item.id)) {
                        elem.classList.add("read");
                    }
                    elem.innerHTML = `<div><a href="${item.link}" onclick="save_read('${item.id}');">${item.title}</a><p>${item.author} ${item.published.toLocaleString()}</p></div>`;
                    return elem;
                })
            );
        } break;
        case "Feeds": {
            result_list.replaceChildren(
                ...feeds
                .map(feed => {
                    let new_feed = {...feed};

                    new_feed.title_matches = 0;
                    new_feed.description_matches = 0;
                    if (search_terms.length !== 0) {
                        new_feed.title = new_feed.title.replace(search_regex, match => { ++new_feed.title_matches; return `<mark>${match}</mark>`; });
                        new_feed.description = new_feed.description.replace(search_regex, match => { ++new_feed.description_matches; return `<mark>${match}</mark>`; });
                    }

                    return new_feed;
                })
                .filter(feed => {
                    return feed.title_matches >= search_terms.length || feed.description_matches >= search_terms.length;
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
                    return b.updated - a.updated
                })
                .map(feed => {
                    const elem = document.createElement("div")
                    elem.classList.add("item");
                    elem.innerHTML = `<div><a href="${feed.link}">${feed.title}</a><p>${feed.description}</p></div>`;
                    return elem;
                })
            );
        } break;
    }
};
