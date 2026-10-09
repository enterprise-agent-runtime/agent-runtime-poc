#!/usr/bin/env bash
# Regenerates all Word files from the Markdown sources. Requires Node.js with the `docx` package (npm install docx) and, for diagrams, Graphviz (dot).
set -e
cd "$(dirname "$0")"
python3 diagrams.py                     # optional: re-render diagrams from the .dot sources
mkdir -p ../out
for f in ../markdown/WRD-*.md; do node md2docx.js "$f" "../out/$(basename "${f%.md}").docx"; done
