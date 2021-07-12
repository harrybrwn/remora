CREATE OR REPLACE VIEW page_h AS
  SELECT ipv4,
         status,
         left(url, 135) as URL,
         array_length(links, 1) as links,
         to_char(crawled_at, 'MM:DD:YYYY HH12:MI:SS AM') as crawled_at,
         depth,
         left(content_type, 16),
         redirected,
         response_time
    FROM page;

CREATE OR REPLACE VIEW page_rank AS
	SELECT page.*,
		   count(DISTINCT edge.parent_id) as rank
	  FROM page,
	       edge
	 WHERE page.id = edge.child_id
  GROUP BY page.id;

CREATE OR REPLACE VIEW page_count AS
  SELECT count(DISTINCT url)
  FROM page;

CREATE MATERIALIZED VIEW page_rankm AS
	SELECT page.*,
		   count(DISTINCT edge.parent_id) as rank
	  FROM page,
	       edge
	 WHERE page.id = edge.child_id
  GROUP BY page.id;

CREATE OR REPLACE VIEW open_clients AS
    SELECT usename,
           client_hostname,
           client_addr,
           client_port,
           backend_start,
           query_start
      FROM pg_stat_activity
     WHERE backend_type = 'client backend'