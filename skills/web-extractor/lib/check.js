// Generic, LLM-free checker for a saved watch. Two ways to invoke:
//
//   First time (from a cron job) — pass the full config as input:
//     cron_create(kind="js", target="skill://web-extractor/lib/check.js",
//       input='{"name":"fzoeu","url":"https://...","selector":"li.accordion-header-natjecaji"}')
//
//   By hand, re-check an existing watch by name:
//     run_js({ script: "skill://web-extractor/lib/check.js", args: { name: "fzoeu" } })
include("skill://web-extractor/lib/extract.js");
runWatch(typeof args !== "undefined" && args ? args : {});
