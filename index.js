function getOrDefault(map, key, defaultValue) {
    let value = map.get(key);
    if (value === undefined) {
        value = defaultValue;
        map.set(key, value);
    }
    return value;
}

function* codePointIndices(string) {
    let i = 0;
    for (const character of string) {
        yield [i, character];
        i += character.length;
    }
}

function* words(string) {
    let wordStart = 0;
    for (const [i, codePoint] of codePointIndices(string)) {
        if (/\s/.test(codePoint)) {
            if (wordStart !== i) {
                const word = string.substring(wordStart, i);
                yield [wordStart, word];
            }

            wordStart = i + codePoint.length;
        }
    }
    if (wordStart !== string.length) {
        const word = string.substring(wordStart);
        yield [wordStart, word];
    }
}

console.time("Fetch");
const rawEntries = await fetch("https://rss.renhult.xyz/entries").then(response => response.json());

const entries = new Map();
for (const entry of rawEntries) {
    entries.set(entry.id, entry);
}
console.timeEnd("Fetch");

console.time("Build index");

// NOTE(simon): Extract all words.
const wordToEntry = new Map();
for (const entry of entries.values()) {
    const title = entry.title;

    for (const [i, word] of words(title)) {
        getOrDefault(wordToEntry, word.toLowerCase(), []).push({ id: entry.id, offset: i, });
    }
}

// NOTE(simon): Build suffix array of words.
const suffixArray = [];
for (const word of wordToEntry.keys()) {
    for (const [i, _] of codePointIndices(word)) {
        suffixArray.push({suffix: word.substring(i), word: word, offset: i, });
    }
}

// NOTE(simon): Sort suffix array for efficient searching.
suffixArray.sort((a, b) => {
    if (a.suffix < b.suffix) {
        return -1;
    } else if (a.suffix > b.suffix) {
        return 1;
    } else {
        return 0;
    }
});
console.timeEnd("Build index");
console.log(`Found ${entries.size} entries, ${wordToEntry.size} words, and ${suffixArray.length} suffixes`);

//const search = "git hub";
const search = "book review";
//const search = "book ook";
// const search = "rust c";

console.time("Search");

// NOTE(simon): Collect matche sets.
const matchSets = new Map();
for (const [_, word] of words(search)) {
    const searchTerm = word.toLowerCase();

    // NOTE(simon): Compute lower bound.
    let low = 0;
    let high = suffixArray.length;
    while (low < high) {
        const middle = Math.floor((low + high) / 2);
        const middleItem = suffixArray[middle].suffix;
        if (middleItem < searchTerm) {
            low = middle + 1;
        } else {
            high = middle;
        }
    }

    // NOTE(simon): Collect matches.
    for (let i = low; i < suffixArray.length && suffixArray[i].suffix.startsWith(searchTerm); ++i) {
        const suffix = suffixArray[i];
        const entries = wordToEntry.get(suffix.word);
        for (const entry of entries) {
            const matchSet = getOrDefault(matchSets, entry.id, []);
            matchSet.push({
                start:      entry.offset + suffix.offset,
                end:        entry.offset + suffix.offset + searchTerm.length,
                startScore: 1 - suffix.offset / (suffix.word.length - searchTerm.length + 1),
                matchScore: searchTerm.length / suffix.word.length,
            });
        }
    }
}

console.timeEnd("Search");

console.log(`Found ${matchSets.size} match sets`);

console.time("Score");
const matchLists = [];
for (const [id, matchSet] of matchSets) {
    // NOTE(simon): Remove overlapping matches with the lowest score.
    const matchList = matchSet.filter(a => matchSet.every(b => {
        if (a.start < b.end && b.start < a.end) {
            return a.matchScore > b.matchScore || (a.matchScore === b.matchScore && a.startScore >= b.startScore);
        } else {
            return true;
        }
    }));

    // NOTE(simon): Sort matches on starting position for visualizations.
    matchList.sort((a, b) => b.start - a.start);

    // NOTE(simon): Matching full words, matching earlier in words, longer matches, and more matches should yield a higher score.
    const score = matchList.length * matchList.map(match => (match.end - match.start) * match.startScore * match.matchScore).reduce((acc, x) => acc + x, 0);

    matchLists.push({ id: id, score: score, matches: matchList, });
}
console.timeEnd("Score");

const average = matchLists.reduce((acc, match) => acc + match.score, 0) / matchLists.length;
const standardDeviation = Math.sqrt(matchLists.reduce((acc, match) => acc + Math.pow(average - match.score, 2), 0) / matchLists.length);
const max = matchLists.reduce((acc, match) => Math.max(acc, match.score), 0);
console.log(`avg: ${average}, std: ${standardDeviation}`);
console.log(`avg cutoff is at ${average - standardDeviation}`);
console.log(`max cutoff is at ${max - standardDeviation}`);

const results = matchLists
    .filter(match => match.score >= average + standardDeviation)
    .map(({ id, score, matches, }) => {
        const entry = entries.get(id);
        return {
            title:     entry.title,
            published: Date.parse(entry.published),
            score:     score,
            matches:   matches,
        };
    })
    .toSorted((a, b) => {
        let result = 0;

        // NOTE(simon): Higher score should appear earlier.
        if (result === 0) {
            result = b.score - a.score
        }

        // NOTE(simon): Newer items should appear earlier.
        if (result === 0) {
            if (a.published > b.published) {
                result = -1;
            } else if (b.published > a.published) {
                result = 1;
            }
        }

        // NOTE(simon): Fallback to lexicographical ordering.
        if (result === 0) {
            if (a.title < b.title) {
                result = -1;
            } else if (b.title < a.title) {
                result = 1;
            }
        }

        return result;
    });

console.log(results.reverse());
