-- RFC 7230 section 3.1.1 states that the recomended
-- minimum request line is 8000 bytes (8000 characters).
--
-- RFC 2181 section 11 states that the maximum domain
-- name length including separators is 255.
--
-- The url prefix "https://" is 8 characters long.
--
-- As a superstitious precausion, this maximum size
-- will be multiplied by 2.
--
-- Also see <https://stackoverflow.com/questions/417142/what-is-the-maximum-length-of-a-url-in-different-browsers>
--
-- (8000 + 255 + 8) * 2 = 16526
CREATE DOMAIN url AS varchar(16526);

CREATE TABLE page (
  id    bytea PRIMARY KEY,
  url   varchar(16526),
  ipv4  inet,
  ipv6  inet,
  links url[],

  crawled_at timestamp,
  depth      int,

  -- HTTP Fields
  redirected      boolean,
  redirected_from url,
  status          int, -- http status code
  -- RFC 6838 section 4.2 states max length
  -- of mime types as 127.
  content_type  varchar(127),
  response_time interval,

  -- All character encodings found here
  -- <https://html.spec.whatwg.org/multipage/parsing.html#character-encodings>.
  -- The maximum length of all allowed encodings is 12.
  chr_encoding varchar(12),

  title    text,
  keywords tsvector,

  UNIQUE (url)
);

CREATE INDEX keyword_idx ON page USING GIN (keywords);

CREATE TABLE edge (
  parent_id bytea,
  child_id  bytea,
  child     url,
  UNIQUE(parent_id, child_id)
);

-- CREATE UNIQUE INDEX idx_edge_parent_child
--   ON edge (parent_id, child_id, child);

CREATE MATERIALIZED VIEW hosts AS
  SELECT
    (regexp_matches(url,
        '^(.*:)//([A-Za-z0-9\-\.]+)(:[0-9]+)?(.*)$'))[2] as host,
    ipv4 as ip,
    count(DISTINCT url) as n
  FROM page
  GROUP BY host, ipv4;

CREATE TABLE node (
  id        BIGSERIAL PRIMARY KEY,  -- should never by zero so we can use it as a null value
  page_id   BYTEA
  -- parent_id BIGINT,
  -- CHECK (id <> parent_id)
);

CREATE TABLE node_edge (
  node_id  BIGINT,
  child_id BIGINT
);

-- WITH new_node AS (
--   INSERT INTO node (parent_id, page_id)
--   VALUES (0, E'\x79ff909872c10ba9d779b84ea0900c2e')
--   RETURNING id
-- )
-- INSERT INTO node (parent_id, page_id)
--   SELECT id, E'\x072366a1b8b480d67498be3000710664'::bytea FROM new_node
-- UNION
--   SELECT id, E'\x79d70ee7b124e48777a9e2ffce04b4df'::bytea FROM new_node;
