// Combines querySelectorAll and forEach. Unlike querySelectorAll it includes
// the root.
function forEachSelector(element, selector, callbackFn) {
    if (element.matches(selector)) {
        callbackFn(element);
    }
    element.querySelectorAll(selector).forEach(callbackFn);
}



const timeFormatter = Intl.DateTimeFormat(undefined, {
    day: "numeric",
    month: "numeric",
    year: "numeric",
    hour: "2-digit",
    minute: "2-digit",
    second: "2-digit",
});

function setLocaleDates(tree = document) {
    forEachSelector(tree, "time[datetime]", timeRoot => {
        timeRoot.innerText = timeFormatter.format(new Date(timeRoot.getAttribute("datetime")));
    });
}

htmx.onLoad(setLocaleDates);



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
