// Build a prioritized list of public-data source URLs for a company.
// Deterministic string work only (no network) — fetching is done by WebFetch.
//
// args: { name?: string, domain?: string }  (domain may be a full URL)
// returns: { name, domain, slug, sources: [{source, url, fields, note}] }

var a = (typeof args !== "undefined" && args) ? args : {};
var name = (a.name == null ? "" : String(a.name)).trim();
var domain = (a.domain == null ? "" : String(a.domain)).trim().toLowerCase();
// Reduce a URL to a bare host: strip scheme, path, and leading www.
domain = domain.replace(/^https?:\/\//, "").replace(/\/.*$/, "").replace(/^www\./, "");

function enc(s) { return encodeURIComponent(s); }
function slug(s) {
  return s.toLowerCase()
    .replace(/&/g, " and ")
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
}

var query = name || domain;
var root = name || (domain ? domain.split(".")[0] : "");
var sl = slug(root);

var sources = [];
function add(source, url, fields, note) {
  sources.push({ source: source, url: url, fields: fields, note: note || "" });
}

// Find the official domain first when only a name is known.
if (!domain && query) {
  add("web-search",
    "https://duckduckgo.com/html/?q=" + enc(query + " official website"),
    "official domain",
    "Use to identify the single official site. If several distinct companies match, ask the user for the URL.");
}

// Wikipedia: opensearch finds the exact article, then fetch that page.
add("wikipedia-search",
  "https://en.wikipedia.org/w/api.php?action=opensearch&limit=5&format=json&search=" + enc(query),
  "candidate article titles + urls",
  "JSON [query,[titles],[descriptions],[urls]]. Pick the company; WebFetch its url.");

if (domain) {
  add("company-about", "https://" + domain + "/about",
    "founders, structure, leadership, history", "If 404, try /about-us, /company, /investors.");
  add("company-home", "https://" + domain + "/", "industry, products", "");
}

add("crunchbase", "https://www.crunchbase.com/organization/" + sl,
  "founders, funding, HQ, industry", "Often login/anti-bot gated; may need the Zyte fallback (auto on 4xx).");
add("linkedin", "https://www.linkedin.com/company/" + sl + "/about/",
  "industry, employee count, HQ", "Usually login-walled; skip if blocked.");
add("opencorporates", "https://opencorporates.com/companies?q=" + enc(query),
  "legal entity, jurisdiction, structure", "Authoritative for legal/registration structure.");
add("sec-edgar",
  "https://www.sec.gov/cgi-bin/browse-edgar?action=getcompany&company=" + enc(query) + "&type=10-K&owner=include&count=10",
  "revenue, structure (US-listed only)", "Authoritative 10-K filings for US public companies.");

return { name: name, domain: domain, slug: sl, sources: sources };
