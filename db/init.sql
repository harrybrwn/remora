CREATE TABLE page (
    -- RFC 7230 section 3.1.1 states that the recomended
    -- minimum request line is 8000 bytes (8000 characters).
    -- RFC 2181 section 11 states that the maximum domain
    -- name length including separators is 255.
    -- The url prefix "https://" is 8 characters long.
    -- As a superstitious precausion, this maximum size
    -- will be multiplied by 2.
    --
    -- Also see <https://stackoverflow.com/questions/417142/what-is-the-maximum-length-of-a-url-in-different-browsers>
    --
    -- (8000 + 255 + 8) * 2 = 16526
    url varchar(16526),
    links text[],

    -- RFC 6838 section 4.2 states max length
    -- of mime types as 127.
    content_type varchar(127),

    crawl_date TIMESTAMP,
    redirected boolean
);
