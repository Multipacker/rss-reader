console.time("Fetch");
const rawEntries = await fetch("https://rss.renhult.xyz/entries").then(response => response.json());

const entries = new Map();
for (const entry of rawEntries) {
    entries.set(entry.id, entry);
}
console.timeEnd("Fetch");

console.time("Encode");
console.time("Build trie");
let trie = new Map();
for (const [_, entry] of entries) {
    const parts = entry.id.split("/");
    let parent = trie;
    for (const part of parts) {
        let child = parent.get(part) ?? new Map();
        parent.set(part, child);
        parent = child;
    }
}
console.timeEnd("Build trie");

console.time("Compress");
const flatten = trie => trie
    .entries()
    .map(([key, values]) => {
        const flattened = flatten(values);

        if (flattened.length === 0) {
            return key;
        } else if (flattened.length === 1) {
            if (flattened[0].length == 2) {
                return [key + "/" + flattened[0][0], flattened[0][1]];
            } else {
                return key + "/" + flattened[0];
            }
        } else {
            return [key, flattened];
        }
    })
    .toArray();
const flattened = flatten(trie)
console.timeEnd("Compress");
console.timeEnd("Encode");

console.log(JSON.stringify(flattened));
//console.dir(flattened, {depth: 20});

const expand = trie => trie
    .flatMap(entry => {
        if (Array.isArray(entry)) {
            return expand(entry[1]).map(part => entry[0] + "/" + part);
        } else {
            return entry;
        }
    });
console.log(expand(flattened));
