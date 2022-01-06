# Crawler API

## Operations

### `GET /crawl`

Params:
- `?url=<url>`

Response:
```json
{
	"url": "https://en.wikipedia.org/wiki/Main_Page",
	"redirected": false,
}
```

### `POST /start`

Request Body:

```json
{
	"max_depth": 2,
	"host": "en.wikipedia.org",
	"routing_key": "<hash>.en.wikipedia.org"
}
```

### `POST /stop`
- `?host=en.wikipedia.org`

### `GET /status`
