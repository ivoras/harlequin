// Generic, LLM-free checker for a saved watch. Run it by name:
//   run_js({ script: "skill://web-extractor/lib/check.js", args: { name: "fzoeu" } })
include("skill://web-extractor/lib/extract.js");
checkWatch(typeof args !== "undefined" && args ? args.name : null);
