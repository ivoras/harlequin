// Reusable helpers for the web-extractor skill. ES5 only (otto sandbox): var,
// no let/const, no arrow functions, no template literals.
//
// Loaded with include("skill://web-extractor/lib/extract.js"), which runs this in
// the global scope so these functions become available to the calling script.

// fetchDoc fetches a URL and returns a parsed DOM handle.
function fetchDoc(url) {
  var resp = fetch(url);
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

// checkWatch runs one LLM-free check of a saved watch: fetch the page, extract the
// items at the recipe's selector, diff against the last seen list, print a concise
// report, and persist the new state.
function checkWatch(name) {
  if (!name) { println("error: no watch name given"); return; }
  var r = loadRecipe(name);
  if (!r) { println("error: no recipe for '" + name + "' (run setup first)"); return; }
  var doc = fetchDoc(r.url);
  var items = allTextAt(doc, r.selector);
  var d = diffList(r.lastSeen, items);
  var label = r.label || name;
  if (d.added.length || d.removed.length) {
    println("CHANGED: " + label + " (" + items.length + " items now)");
    var i;
    for (i = 0; i < d.added.length; i++) println("  + " + d.added[i]);
    for (i = 0; i < d.removed.length; i++) println("  - " + d.removed[i]);
  } else {
    println("No change: " + label + " (" + items.length + " items)");
  }
  r.lastSeen = items;
  r.lastChecked = nowISO();
  saveRecipe(name, r);
}
