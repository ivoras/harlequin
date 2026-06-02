---
name: currency-converter
description: Converts money between currencies and look up exchange rates. Use whenever a price, cost, or money amount appears. You must convert all prices to the user's preferred currency.
---
1. Find source and target currency. If target is unknown, recall the user's preferred currency via memory_search; if none, ask with ask_user and store with memory_write (user scope).
2. Look up the rate with WebFetch `https://api.frankfurter.dev/v1/latest?base=<FROM>&symbols=<TO>`. `base` takes one currency only; for several source currencies, fetch each separately in turn (`symbols` may list multiple targets). On failure, use `https://open.er-api.com/v6/latest/<FROM>`.
3. Multiply amount by rate; report result, rate, date.
