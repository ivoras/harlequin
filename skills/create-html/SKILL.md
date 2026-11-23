---
name: create-html
description: Create a self-contained HTML page (.html) the user can download, open in any browser or send on — a report, summary, table or page of notes written by you, or an existing text document turned into a web page. load_skill this whenever the user asks for an HTML file or page, "as HTML", or something to open in the browser. It explains the Markdown (and, for special layouts, HTML) the create_html tool turns into a page.
---

# Create an HTML page

You write the content as ordinary Markdown and `create_html` wraps it in a clean,
readable page: headings, paragraphs, **bold**, lists, pipe tables, quotes and code
blocks all work. The page is one self-contained file that opens offline. The tool
removes scripts, forms and every external image, font or stylesheet, so the page
cannot run code or load anything from the web (inline `data:` images stay). You
do not design the page.

It is the same pipeline as `create_pdf` and `create_word`, with the same
parameters: `title`, `content` (or `source`: a sandbox file such as
`tmp://report.md`, or a saved document `u.12`), `format`, `scope`, `save_to` and
`overwrite`. There is no `landscape` (a web page has no paper size).

## Steps

1. Write the Markdown. Start with `# Heading`: it is the page's visible title. A
   long page: build it in a sandbox file with `edit_file` `append`, then pass
   `source`.
2. Only when the user needs a layout Markdown cannot express (colours, columns,
   spacing), write HTML with your own CSS in a `<style>` element and set
   `format: "html"`.
3. Choose where the result goes (below), call `create_html`, and report.

## Where does the result go?

- **A saved document** (the default): it appears in the user's files (the
  project's, in a project session) and can be changed later with `edit_doc`
  (replace, insert, append): the page is rebuilt from its source.
- **A temporary file**: `save_to: "tmp://report.html"` and write that path in
  your reply: the user gets a **download link**. It is removed after a while and
  not in their files; `copy_doc` it (no `target`) to keep it.

If the user did not say whether they want to keep it in their files or only
download it, ask with `ask_user` in one short question. Do not ask when the
request already says ("save it", "put it in my files" mean a saved document;
"give me the file" or "I just want to download it" mean temporary).

## Report

Say the document's id and title, or the `tmp://` path, in one or two sentences.
If the result lists notes (images or scripts that were removed), tell the user.
To change a saved page later, use `edit_doc` (load_skill "edit-document"), not
`create_html` again.
