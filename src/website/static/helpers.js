// Combines querySelectorAll and forEach. Unlike querySelectorAll it includes
// the root.
function forEachSelector(element, selector, callbackFn) {
    if (element.matches(selector)) {
        callbackFn(element);
    }
    element.querySelectorAll(selector).forEach(callbackFn);
}
