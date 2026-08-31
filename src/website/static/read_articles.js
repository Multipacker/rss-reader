let readArticles = new Set(JSON.parse(localStorage.getItem("read_articles")));

function filterReadStatus(tree = document) {
    const searchFilter = document.getElementById("search_filter").value;

    forEachSelector(tree, "[data-entry-id]", itemRoot => {
        const id = itemRoot.getAttribute("data-entry-id");
        const isRead = readArticles.has(id);

        if ((searchFilter === "Read" && !isRead) || (searchFilter === "Unread" && isRead)) {
            itemRoot.remove();
        }

        if (isRead) {
            itemRoot.classList.add("read");
        }

        htmx.on(itemRoot, "click", () => {
            itemRoot.classList.add("read");
            readArticles.add(id);
            localStorage.setItem("read_articles", JSON.stringify([...readArticles.values()]));
        });
    });
}

htmx.onLoad(filterReadStatus);
