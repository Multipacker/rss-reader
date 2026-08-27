CREATE TABLE Feeds (
    id           UUID NOT NULL PRIMARY KEY DEFAULT uuidv7(),
    externalId   TEXT NOT NULL UNIQUE,
    title        TEXT NOT NULL,
    description  TEXT NOT NULL,
    url          TEXT NOT NULL,
    updated      TIMESTAMPTZ NOT NULL
);

CREATE TABLE Entries (
    id          UUID NOT NULL PRIMARY KEY DEFAULT uuidv7(), 
    feed        UUID NOT NULL REFERENCES Feeds(id),
    externalId  TEXT NOT NULL UNIQUE,
    title       TEXT NOT NULL,
    url         TEXT NOT NULL,
    published   TIMESTAMPTZ NOT NULL,
    updated     TIMESTAMPTZ NOT NULL
);

CREATE TABLE UnfetchedSnapshots (
    feed UUID NOT NULL REFERENCES Feeds(id),
    date TEXT NOT NULL,
    PRIMARY KEY (feed, date)
);
