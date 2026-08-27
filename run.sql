DROP TABLE UnfetchedSnapshots, Entries, Feeds;

\i sql/init.sql

INSERT INTO Feeds (externalId, title, description, url, updated) VALUES
    ('0', 'title0', 'description0', 'url0', CURRENT_TIMESTAMP),
    ('1', 'title1', 'description1', 'url1', CURRENT_TIMESTAMP),
    ('2', 'title2', 'description2', 'url2', CURRENT_TIMESTAMP),
    ('3', 'title3', 'description3', 'url3', CURRENT_TIMESTAMP),
    ('4', 'title4', 'description4', 'url4', CURRENT_TIMESTAMP),
    ('5', 'title5', 'description5', 'url5', CURRENT_TIMESTAMP),
    ('6', 'title6', 'description6', 'url6', CURRENT_TIMESTAMP),
    ('7', 'title7', 'description7', 'url7', CURRENT_TIMESTAMP),
    ('8', 'title8', 'description8', 'url8', CURRENT_TIMESTAMP),
    ('9', 'title9', 'description9', 'url9', CURRENT_TIMESTAMP);
