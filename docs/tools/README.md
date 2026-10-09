# Tools

- `md2docx.js` converts one Markdown file into a styled Word document (cover page, table of contents, headers/footers, tables, code blocks, images, callouts).
  Usage: `node md2docx.js ../markdown/WRD-01-MVP-Product-Requirements.md ../out/WRD-01-MVP-Product-Requirements.docx`
- `diagrams.py` renders the Graphviz `.dot` sources in `../markdown/img/` to PNG (requires `dot`).
- `build.sh` regenerates everything into `../out/`.

Markdown subset understood by the converter: YAML front matter (title, subtitle, docid, version, status, date, owner, audience), headings `#` to `####`, paragraphs, `-` bullets, numbered lists, pipe tables, fenced code blocks, images `![caption](img/x.png)`, `>` callouts, `**bold**`, `*italic*`, `` `code` ``.
