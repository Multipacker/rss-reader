CREATE TABLE Feeds (
    -- NOTE(simon): Feed data
    id          TEXT NOT NULL PRIMARY KEY,
    title       TEXT NOT NULL,
    description TEXT NOT NULL,
    link        TEXT NOT NULL,
    updated     TIMESTAMP WITH TIME ZONE NOT NULL,

    -- NOTE(simon): HTTP metadata
    etag TEXT NOT NULL
);

CREATE TABLE Entries (
    id        TEXT NOT NULL,
    feed      TEXT NOT NULL,
    title     TEXT NOT NULL,
    published TIMESTAMP WITH TIME ZONE NOT NULL,
    updated   TIMESTAMP WITH TIME ZONE NOT NULL,
    link      TEXT NOT NULL,
    PRIMARY KEY (id, feed)
);
