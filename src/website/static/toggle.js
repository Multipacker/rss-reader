function addToggle(tree = document) {
    forEachSelector(tree, "[data-toggle-for]", toggle => {
        htmx.on(toggle, "click", () => {
            document.querySelector(toggle.getAttribute("data-toggle-for")).classList.toggle("toggle-open");
        });
    });
}

htmx.onLoad(addToggle);
