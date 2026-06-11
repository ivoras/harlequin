// Reusable helpers for the web-extractor skill. Runs in the jsrun sandbox (goja,
// ES5.1-compatible; common ES6 such as let/const, arrow functions and template
// literals also works). These helpers stay in plain var-style for simplicity.
//
// Loaded with include("skill://web-extractor/lib/extract.js"), which runs this in
// the global scope so these functions become available to the calling script.

// fetchDoc fetches a URL and returns a parsed DOM handle. It THROWS on a non-2xx
// response so a transient failure (timeout, 403/anti-bot, 5xx) surfaces as a job
// error rather than a fake "the page changed" — the scheduler does not notify on
// errors and keeps the last good baseline.
function fetchDoc(url) {
  var resp = fetch(url);
  var st = resp.status || 0;
  if (st < 200 || st >= 300) {
    throw new Error("fetch " + url + " returned HTTP " + st);
  }
  if (!resp.body || !trim(resp.body)) {
    throw new Error("fetch " + url + " returned an empty body");
  }
  return dom.parse(resp.body);
}

function trim(s) { return ("" + s).replace(/^\s+|\s+$/g, ""); }

// allTextAt returns the trimmed text of every element matching selector.
function allTextAt(doc, selector) {
  var nodes = dom.query(doc, selector);
  var out = [];
  for (var i = 0; i < nodes.length; i++) {
    var t = nodes[i].text ? trim(nodes[i].text) : "";
    if (t) out.push(t);
  }
  return out;
}

// attrAt returns the given attribute of every element matching selector.
function attrAt(doc, selector, attr) {
  var nodes = dom.query(doc, selector);
  var out = [];
  for (var i = 0; i < nodes.length; i++) {
    var a = nodes[i].attrs && nodes[i].attrs[attr];
    if (a) out.push(a);
  }
  return out;
}

// recipePath / loadRecipe / saveRecipe persist a watch's config + last state in
// the per-user storage area.
function recipePath(name) { return name + "/recipe.json"; }

function loadRecipe(name) {
  if (!storage.exists(recipePath(name))) return null;
  return JSON.parse(storage.read(recipePath(name)));
}

function saveRecipe(name, recipe) {
  storage.write(recipePath(name), JSON.stringify(recipe));
}

// diffList compares two arrays of strings, returning the added and removed items.
function diffList(oldArr, newArr) {
  oldArr = oldArr || [];
  newArr = newArr || [];
  var oldSet = {}, newSet = {}, i;
  for (i = 0; i < oldArr.length; i++) oldSet[oldArr[i]] = true;
  for (i = 0; i < newArr.length; i++) newSet[newArr[i]] = true;
  var added = [], removed = [];
  for (i = 0; i < newArr.length; i++) if (!oldSet[newArr[i]]) added.push(newArr[i]);
  for (i = 0; i < oldArr.length; i++) if (!newSet[oldArr[i]]) removed.push(oldArr[i]);
  return { added: added, removed: removed };
}

function nowISO() {
  try { return new Date().toISOString(); } catch (e) { return "" + (new Date()).getTime(); }
}

// runWatch performs one LLM-free check from a config object
// {name, url, selector, label}. On the first run it creates the recipe from
// url+selector; later runs reuse the saved recipe (cfg fields override it). It
// fetches the page, extracts the items at the selector, diffs against the last
// seen list, prints a concise report, and persists the new state. This is what a
// cron job runs: cron passes {name,url,selector} as input the first time, and just
// {name} (from the saved recipe) is enough thereafter.
function runWatch(cfg) {
  cfg = cfg || {};
  var name = cfg.name;
  if (!name) { println("error: missing 'name' in input"); return; }
  var r = loadRecipe(name);
  if (!r) {
    if (!cfg.url || !cfg.selector) {
      println("error: first run of '" + name + "' needs 'url' and 'selector' in input");
      return;
    }
    r = { url: cfg.url, selector: cfg.selector, label: cfg.label || name, lastSeen: [], lastChecked: "" };
  } else {
    if (cfg.url) r.url = cfg.url;
    if (cfg.selector) r.selector = cfg.selector;
    if (cfg.label) r.label = cfg.label;
  }
  var doc = fetchDoc(r.url);
  var items = allTextAt(doc, r.selector);
  // Anti-flap: an established watch suddenly matching nothing is almost always a
  // broken fetch/selector or an anti-bot page, not a real "everything removed".
  // Throw (job error, no alert, baseline kept) instead of reporting a huge change.
  if (items.length === 0 && r.lastChecked && r.lastSeen.length > 0) {
    throw new Error("selector '" + r.selector + "' matched no items (page/selector/anti-bot issue?)");
  }
  var d = diffList(r.lastSeen, items);
  var label = r.label || name;
  if (!r.lastChecked) {
    println("Watching " + label + ": " + items.length + " item(s) now (baseline saved).");
  } else if (d.added.length || d.removed.length) {
    println("CHANGED: " + label + " (" + items.length + " item(s) now)");
    var i;
    for (i = 0; i < d.added.length; i++) println("  + " + d.added[i]);
    for (i = 0; i < d.removed.length; i++) println("  - " + d.removed[i]);
  } else {
    println("No change: " + label + " (" + items.length + " item(s))");
  }
  r.lastSeen = items;
  r.lastChecked = nowISO();
  saveRecipe(name, r);
}

// checkWatch runs a saved watch by name (its config is already persisted).
function checkWatch(name) { runWatch({ name: name }); }
