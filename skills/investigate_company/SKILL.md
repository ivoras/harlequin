---
name: investigate_company
description: Investigate a company from its name or URL using public sources (Wikipedia, the company's own site, Crunchbase, LinkedIn, SEC EDGAR, OpenCorporates) and report founders, headquarters, industry, major achievements, revenue, and company structure; records findings to memory unless told not to. load_skill this whenever asked to research, look up, profile, or investigate a company or organisation.
---

# Investigate a company

Goal: from a company **name or URL**, collect public facts and report them:

- **founders** (names, founding year)
- **headquarters** (city + country)
- **industry** (what the business does)
- **major achievements** (products, milestones, awards, notable events)
- **revenue** (amount + currency + fiscal year)
- **company structure** (public/private, parent company, subsidiaries, employee count)

Rules: report only facts found in sources; never guess or use internal knowledge. Cite the source for each fact. If a field is not found, write "not found".

## Step 1 — Identify the company

- First `memory_search` the name/domain — it may already be recorded; reuse and skip fetching what you already have.
- If given a **URL/domain**, use it directly.
- If given only a **name**, build sources (Step 2) and fetch `web-search` + `wikipedia-search` to find the single official domain and Wikipedia article.
- **Ambiguous** = the name matches several distinct companies, or no single official site is identifiable. Then `ask_user` for the company's URL or domain. Do not pick between candidates yourself.

## Step 2 — Build source URLs

Call the shipped builder (do not invent URLs by hand):

    run_js({ script: "skill://investigate_company/lib/sources.js",
             args: { name: "<name>", domain: "<domain, or empty if unknown>" } })

It returns `sources`: a prioritized list of `{source, url, fields, note}`. Use those URLs verbatim.

## Step 3 — Fetch sources (priority order)

Fetch with **WebFetch** (returns Markdown). Take fields in this order and stop once each target field is filled from a reliable source:

1. **wikipedia-search**: opensearch JSON `[query,[titles],[descriptions],[urls]]`. Pick the title that matches the company; WebFetch its url. Best single source: founders, HQ, industry, founding year, key people, revenue (cited), parent/subsidiaries.
2. **company-about** / **company-home**: founders, leadership, structure, achievements, products. If `/about` 404s, try `/about-us`, `/company`, `/investors`.
3. **crunchbase**: founders, funding, HQ, industry. Often gated; if it returns an error the Zyte fallback may recover it.
4. **linkedin**: industry, employee count, HQ. Usually login-walled — skip if blocked.
5. **opencorporates**: legal entity, jurisdiction, corporate structure.
6. **sec-edgar**: US-listed companies only — authoritative revenue/structure from 10-K filings.

Use **WebFetchDOM** only if a page needs structured extraction that WebFetch's Markdown loses.

## Step 4 — Report

Present a Markdown table: **Field | Value | Source**. For revenue, include amount, currency, and fiscal year. If sources disagree, report both values with their sources.

## Step 5 — Record to memory (default ON)

Unless the user said **not** to record: `memory_write` each found fact, **shared** scope (org-wide; if `memory_write` refuses shared, use user scope). Put the company name in every fact so it is searchable. One concise fact per memory, e.g.:

- "Acme Ltd was founded in 2009 by Jane Roe and John Doe. Source: Wikipedia."
- "Acme Ltd headquarters: Berlin, Germany."
- "Acme Ltd industry: industrial robotics."
- "Acme Ltd FY2023 revenue: EUR 120M. Source: annual report."
- "Acme Ltd structure: private; parent Acme Holding GmbH; ~400 employees."

Skip facts already returned by the Step 1 `memory_search`.

## Data sources reference

- **Wikipedia** (`en.wikipedia.org`): public, reliable. Founders, HQ, industry, founding year, key people, revenue (with citations), parent/subsidiaries. Use the opensearch API to find the exact article, then fetch it.
- **Company website** (`/about`, `/about-us`, `/company`, `/investors`, `/press`): founders, mission, leadership, structure, achievements, products.
- **Crunchbase** (`crunchbase.com/organization/<slug>`): founders, funding rounds, HQ, industry. Frequently anti-bot/login gated.
- **LinkedIn** (`linkedin.com/company/<slug>`): industry, employee count, HQ. Usually requires login; often unavailable.
- **SEC EDGAR** (`sec.gov`): authoritative for **US public** companies — revenue, officers, structure (10-K/10-Q).
- **OpenCorporates** (`opencorporates.com`): legal entity, registration jurisdiction, corporate structure.
- **Annual reports / investor relations / reputable news**: revenue figures and major achievements.

## Notes

- `sources.js` only builds URLs (no network); all fetching is via WebFetch/WebFetchDOM. The sandbox is goja (ES5.1 + common ES6).
- Never fabricate founders, revenue, or other values. Prefer a cited figure over an uncited one.
