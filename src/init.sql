CREATE TABLE Feeds (
    id          TEXT NOT NULL PRIMARY KEY,
    title       TEXT NOT NULL,
    description TEXT NOT NULL,
    link        TEXT NOT NULL,
    updated     TIMESTAMP WITH TIME ZONE NOT NULL
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

CREATE TABLE Etags (
    feed TEXT NOT NULL REFERENCES Feeds(id),
    etag TEXT NOT NULL,
    PRIMARY KEY (feed, etag)
);
