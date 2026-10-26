# Dram to toman: Telegram Mini App

Converts an Armenian dram price into USD and Toman using several rate sources
side by side, so you can see the spread. Built for comparing used-phone prices
in Yerevan with Iran.

## Run locally

```bash
cp .env.example .env        # optional
docker compose up --build
```

Open http://localhost:8080 in a browser. Without Docker: `go run .`

Quick API checks:

```bash
curl localhost:8080/api/rates
curl "localhost:8080/api/convert?amd=130000&toman=165000"
```

## Rate sources

| Source | How it is read | Notes |
| --- | --- | --- |
| Market rate | `open.er-api.com/v6/latest/USD` | Free, no key, updates daily. Attribution link is in the page footer (required). Stand-in for the Google rate. |
| Central Bank of Armenia | SOAP call `ExchangeRatesLatest` at `api.cba.am/exchangerates.asmx` | Official daily reference rate. |
| SAS Supermarket | Reads the exchange table from the `sas.am` home page HTML | No API exists. Gives a buy and a sell rate. |

Each source is cached on its own (`CACHE_TTL`, default 10 minutes). If one
fails, the others still work and the failed one shows its last known value or
"could not fetch". Failures are logged with the reason (`docker compose logs app`).

**SAS columns:** "you buy dollars" uses SAS's sell rate (higher), "you sell
dollars" uses their buy rate (lower).

## Dollar to toman rate

Type it in the app; it is saved on the device. To prefill it, set `USD_TOMAN`
in `.env`. Reading it from Telegram channels is not built yet.

## Open it inside Telegram

Telegram only opens HTTPS pages, so localhost will not work directly.

1. `docker compose --profile tunnel up --build`
2. `docker compose logs tunnel` and copy the `https://….trycloudflare.com` URL
   (it changes each time you start the tunnel).
3. In @BotFather: create a bot with `/newbot`, then `/newapp` (or
   `/setmenubutton`) and paste that URL.

The page uses your Telegram theme colors automatically.

## Deploy for free on Render

1. Push this folder to a GitHub repo.
2. In Render: **New > Blueprint**, connect the repo, and click **Apply**.
   `render.yaml` sets up a free Docker web service in Frankfurt with the
   `/healthz` health check.
3. When the deploy finishes, copy the `https://….onrender.com` URL and use it
   in BotFather (`/newapp` or `/setmenubutton`). No tunnel is needed.

Free instances sleep after about 15 minutes without traffic, so the first open
after a quiet period can take 30+ seconds. A free uptime pinger hitting
`/healthz` every 10 minutes keeps it awake. Change `USD_TOMAN` later in the
Render dashboard under Environment.

## Files

- `main.go`: HTTP server and endpoints
- `sources.go`: the three fetchers and their parsers
- `rates.go`: per-source cache and the conversion logic
- `rates_test.go`: parser, cache and conversion tests (`go test ./...`)
- `static/index.html`: the Mini App page (embedded into the binary)

## Notes

- The API has no login. It only serves public rates, but do not expose it widely.
- If SAS redesigns their page, the parser will report "USD row not found".
  Update `parseSAS` in `sources.go`.
