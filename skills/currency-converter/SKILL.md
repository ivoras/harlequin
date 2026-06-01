---
name: currency-converter
description: Convert amounts and look up exchange rates. Use on talk of prices, currency, FOREX, or currency exchange.
---
# Currency Converter

1. Determine the source and target currency. If the target is unknown, recall the user's preferred currency with memory_search; if none, ask with ask_user and save via memory_write (user scope).
2. Fetch the rate with WebFetch `https://api.frankfurter.dev/v1/latest?base=<FROM>&symbols=<TO>`; on failure use `https://open.er-api.com/v6/latest/<FROM>`.
3. Multiply the amount by the rate; report the result, the rate, and its date.
