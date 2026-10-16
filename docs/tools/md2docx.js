// md2docx.js — converts a constrained Markdown subset into a styled .docx using docx-js
// Usage: node md2docx.js input.md output.docx
const fs = require('fs');
const path = require('path');
const {
  Document, Packer, Paragraph, TextRun, HeadingLevel, Table, TableRow, TableCell,
  WidthType, AlignmentType, BorderStyle, ShadingType, ImageRun, PageBreak,
  TableOfContents, Header, Footer, PageNumber, LevelFormat, VerticalAlign,
} = require('docx');

const [,, inFile, outFile] = process.argv;
const src = fs.readFileSync(inFile, 'utf8');
const baseDir = path.dirname(inFile);

// ---------- front matter ----------
let meta = {};
let body = src;
const fm = src.match(/^---\n([\s\S]*?)\n---\n/);
if (fm) {
  fm[1].split('\n').forEach(l => { const m = l.match(/^(\w+):\s*(.*)$/); if (m) meta[m[1]] = m[2].trim(); });
  body = src.slice(fm[0].length);
}
const TITLE = meta.title || 'Untitled';
const SUBTITLE = meta.subtitle || '';
const DOCID = meta.docid || '';
const VERSION = meta.version || '0.4';
const STATUS = meta.status || 'Draft';
const DATE = meta.date || 'September 25, 2026';
const PROGRAM = 'Enterprise AI Agent Runtime Platform (working codename: Warden)';

// ---------- helpers ----------
const FONT = 'Calibri';
const CODEFONT = 'Consolas';
const ACCENT = '1F3A5F';
const LIGHT = 'EAF0F6';
const CODEBG = 'F3F4F6';
const CALLOUTBG = 'FFF7E6';
const CONTENT_WIDTH = 9000; // DXA, A4 with 1" margins ~ 9026

function pngSize(buf) {
  if (buf.slice(1, 4).toString() === 'PNG') return { w: buf.readUInt32BE(16), h: buf.readUInt32BE(20) };
  return { w: 800, h: 500 };
}

// inline markdown: **bold**, *italic*, `code`
function inline(text, extra = {}) {
  const runs = [];
  const re = /(\*\*[^*]+\*\*|`[^`]+`|\*[^*]+\*)/g;
  let last = 0; let m;
  while ((m = re.exec(text)) !== null) {
    if (m.index > last) runs.push(new TextRun({ text: text.slice(last, m.index), font: FONT, ...extra }));
    const t = m[0];
    if (t.startsWith('**')) runs.push(new TextRun({ text: t.slice(2, -2), bold: true, font: FONT, ...extra }));
    else if (t.startsWith('`')) runs.push(new TextRun({ text: t.slice(1, -1), font: CODEFONT, size: 19, shading: { type: ShadingType.CLEAR, fill: CODEBG }, ...extra }));
    else runs.push(new TextRun({ text: t.slice(1, -1), italics: true, font: FONT, ...extra }));
    last = m.index + t.length;
  }
  if (last < text.length) runs.push(new TextRun({ text: text.slice(last), font: FONT, ...extra }));
  return runs;
}

const children = [];
let numInstance = 0;

function para(text, opts = {}) { children.push(new Paragraph({ children: inline(text), spacing: { after: 120 }, ...opts })); }

function heading(text, level) {
  const lv = [HeadingLevel.HEADING_1, HeadingLevel.HEADING_2, HeadingLevel.HEADING_3, HeadingLevel.HEADING_4][level - 1];
  children.push(new Paragraph({ text, heading: lv, spacing: { before: level === 1 ? 360 : 240, after: 120 } }));
}

function codeBlock(lines) {
  lines.forEach((l, i) => {
    children.push(new Paragraph({
      children: [new TextRun({ text: l.length ? l : ' ', font: CODEFONT, size: 18 })],
      shading: { type: ShadingType.CLEAR, fill: CODEBG },
      spacing: { before: 0, after: 0, line: 260 },
      indent: { left: 200, right: 200 },
      keepLines: true,
      border: i === 0 ? { top: { style: BorderStyle.SINGLE, size: 4, color: 'D0D5DB' } } :
              i === lines.length - 1 ? { bottom: { style: BorderStyle.SINGLE, size: 4, color: 'D0D5DB' } } : undefined,
    }));
  });
  children.push(new Paragraph({ text: '', spacing: { after: 80 } }));
}

function callout(lines) {
  lines.forEach((l, i) => {
    children.push(new Paragraph({
      children: inline(l),
      shading: { type: ShadingType.CLEAR, fill: CALLOUTBG },
      border: { left: { style: BorderStyle.SINGLE, size: 24, color: 'E0A526' } },
      indent: { left: 300, right: 200 },
      spacing: { before: 0, after: i === lines.length - 1 ? 160 : 40 },
    }));
  });
}

function bullet(text, level) {
  children.push(new Paragraph({ children: inline(text), numbering: { reference: 'bullets', level }, spacing: { after: 60 } }));
}
function numbered(text, level, instance) {
  children.push(new Paragraph({ children: inline(text), numbering: { reference: 'numbers', level, instance }, spacing: { after: 60 } }));
}

function table(rows) {
  const header = rows[0];
  const cols = header.length;
  // width heuristic: proportional to max content length per column, clamped
  const lens = header.map((_, c) => Math.max(...rows.map(r => (r[c] || '').length), 6));
  const total = lens.reduce((a, b) => a + b, 0);
  let widths = lens.map(l => Math.max(1400, Math.round(CONTENT_WIDTH * l / total)));
  const sum = widths.reduce((a, b) => a + b, 0);
  widths = widths.map(w => Math.round(w * CONTENT_WIDTH / sum));
  const diff = CONTENT_WIDTH - widths.reduce((a, b) => a + b, 0); widths[widths.length - 1] += diff;
  const border = { style: BorderStyle.SINGLE, size: 4, color: 'C9CFD6' };
  const borders = { top: border, bottom: border, left: border, right: border };
  const trows = rows.map((r, ri) => new TableRow({
    tableHeader: ri === 0,
    cantSplit: true,
    children: header.map((_, c) => new TableCell({
      width: { size: widths[c], type: WidthType.DXA },
      borders,
      shading: ri === 0 ? { type: ShadingType.CLEAR, fill: ACCENT } : (ri % 2 === 0 ? { type: ShadingType.CLEAR, fill: 'F7F9FB' } : undefined),
      verticalAlign: VerticalAlign.CENTER,
      margins: { top: 60, bottom: 60, left: 100, right: 100 },
      children: [new Paragraph({ children: inline(r[c] || '', ri === 0 ? { bold: true, color: 'FFFFFF', size: 20 } : { size: 20 }), spacing: { after: 0 } })],
    })),
  }));
  children.push(new Table({ rows: trows, columnWidths: widths, width: { size: CONTENT_WIDTH, type: WidthType.DXA } }));
  children.push(new Paragraph({ text: '', spacing: { after: 120 } }));
}

function image(caption, rel) {
  const p = path.resolve(baseDir, rel);
  if (!fs.existsSync(p)) { para(`[missing image: ${rel}]`); return; }
  const data = fs.readFileSync(p);
  const { w, h } = pngSize(data);
  const maxW = 610; const maxH = 720;
  let scale = Math.min(maxW / w, maxH / h, 1);
  // graphviz renders at 160dpi; scale to 96dpi visual size
  scale = Math.min(scale, 0.62);
  const width = Math.round(w * scale), height = Math.round(h * scale);
  children.push(new Paragraph({ children: [new ImageRun({ type: 'png', data, transformation: { width, height } })], alignment: AlignmentType.CENTER, spacing: { before: 120, after: 60 }, keepNext: true }));
  if (caption) children.push(new Paragraph({ children: [new TextRun({ text: caption, italics: true, size: 18, color: '555555', font: FONT })], alignment: AlignmentType.CENTER, spacing: { after: 200 } }));
}

// ---------- parse ----------
if (!body.includes('[[TOC]]')) body = '[[TOC]]\n' + body;
const lines = body.split('\n');
let i = 0; let paraBuf = [];
function flushPara() { if (paraBuf.length) { para(paraBuf.join(' ')); paraBuf = []; } }
let lastListType = null;
while (i < lines.length) {
  const line = lines[i];
  if (line.startsWith('```')) {
    flushPara(); const buf = []; i++;
    while (i < lines.length && !lines[i].startsWith('```')) { buf.push(lines[i]); i++; }
    i++; codeBlock(buf); lastListType = null; continue;
  }
  if (line.trim() === '[[TOC]]') {
    flushPara();
    children.push(new Paragraph({ text: 'Contents', heading: HeadingLevel.HEADING_1, spacing: { after: 200 } }));
    children.push(new TableOfContents('Contents', { hyperlink: true, headingStyleRange: '1-2' }));
    children.push(new Paragraph({ children: [new PageBreak()] }));
    i++; continue;
  }
  if (line.trim() === '\\pagebreak') { flushPara(); children.push(new Paragraph({ children: [new PageBreak()] })); i++; continue; }
  const h = line.match(/^(#{1,4})\s+(.*)$/);
  if (h) { flushPara(); heading(h[2], h[1].length); i++; lastListType = null; continue; }
  const img = line.match(/^!\[(.*?)\]\((.*?)\)\s*$/);
  if (img) { flushPara(); image(img[1], img[2]); i++; continue; }
  if (line.startsWith('|')) {
    flushPara(); const rows = [];
    while (i < lines.length && lines[i].startsWith('|')) {
      const cells = lines[i].trim().replace(/^\|/, '').replace(/\|$/, '').split('|').map(c => c.trim());
      if (!cells.every(c => /^:?-{2,}:?$/.test(c))) rows.push(cells);
      i++;
    }
    table(rows); lastListType = null; continue;
  }
  if (line.startsWith('>')) {
    flushPara(); const buf = [];
    while (i < lines.length && lines[i].startsWith('>')) { buf.push(lines[i].replace(/^>\s?/, '')); i++; }
    callout(buf); continue;
  }
  const bl = line.match(/^(\s*)[-*]\s+(.*)$/);
  if (bl) { flushPara(); bullet(bl[2], Math.min(2, Math.floor(bl[1].length / 2))); i++; lastListType = 'b'; continue; }
  const nl = line.match(/^(\s*)\d+\.\s+(.*)$/);
  if (nl) {
    flushPara();
    const level = Math.min(2, Math.floor(nl[1].length / 3));
    if (lastListType !== 'n' && level === 0) numInstance++;
    numbered(nl[2], level, numInstance); i++; lastListType = 'n'; continue;
  }
  if (line.trim() === '') { flushPara(); i++; if (lastListType) lastListType = null; continue; }
  paraBuf.push(line.trim()); i++;
}
flushPara();

// ---------- cover page ----------
const cover = [
  new Paragraph({ text: '', spacing: { after: 2400 } }),
  new Paragraph({ children: [new TextRun({ text: PROGRAM, font: FONT, size: 22, color: '666666' })], spacing: { after: 400 } }),
  new Paragraph({ children: [new TextRun({ text: TITLE, font: FONT, size: 56, bold: true, color: ACCENT })], spacing: { after: 240 } }),
  new Paragraph({ children: [new TextRun({ text: SUBTITLE, font: FONT, size: 28, color: '333333' })], spacing: { after: 1200 } }),
];
const metaRows = [
  ['Document ID', DOCID], ['Version', VERSION], ['Status', STATUS], ['Date', DATE],
  ['Owner', meta.owner || 'Product / Architecture'], ['Audience', meta.audience || 'Founders, architects, engineers, security'],
];
const mb = { style: BorderStyle.SINGLE, size: 4, color: 'C9CFD6' };
cover.push(new Table({
  columnWidths: [2400, 6600], width: { size: 9000, type: WidthType.DXA },
  rows: metaRows.map(r => new TableRow({ children: [
    new TableCell({ width: { size: 2400, type: WidthType.DXA }, borders: { top: mb, bottom: mb, left: mb, right: mb }, shading: { type: ShadingType.CLEAR, fill: LIGHT }, margins: { top: 60, bottom: 60, left: 100, right: 100 }, children: [new Paragraph({ children: [new TextRun({ text: r[0], bold: true, font: FONT, size: 20 })] })] }),
    new TableCell({ width: { size: 6600, type: WidthType.DXA }, borders: { top: mb, bottom: mb, left: mb, right: mb }, margins: { top: 60, bottom: 60, left: 100, right: 100 }, children: [new Paragraph({ children: [new TextRun({ text: r[1], font: FONT, size: 20 })] })] }),
  ] })),
}));
cover.push(new Paragraph({ children: [new PageBreak()] }));

// ---------- document ----------
const doc = new Document({
  creator: 'Product & Architecture',
  title: TITLE,
  description: SUBTITLE,
  features: { updateFields: true },
  styles: {
    default: { document: { run: { font: FONT, size: 22 } } },
    paragraphStyles: [
      { id: 'Heading1', name: 'Heading 1', basedOn: 'Normal', next: 'Normal', quickFormat: true, run: { size: 32, bold: true, color: ACCENT, font: FONT }, paragraph: { spacing: { before: 360, after: 120 }, outlineLevel: 0 } },
      { id: 'Heading2', name: 'Heading 2', basedOn: 'Normal', next: 'Normal', quickFormat: true, run: { size: 26, bold: true, color: ACCENT, font: FONT }, paragraph: { spacing: { before: 240, after: 100 }, outlineLevel: 1 } },
      { id: 'Heading3', name: 'Heading 3', basedOn: 'Normal', next: 'Normal', quickFormat: true, run: { size: 23, bold: true, color: '2E5077', font: FONT }, paragraph: { spacing: { before: 200, after: 80 }, outlineLevel: 2 } },
      { id: 'Heading4', name: 'Heading 4', basedOn: 'Normal', next: 'Normal', quickFormat: true, run: { size: 22, bold: true, italics: true, color: '2E5077', font: FONT }, paragraph: { spacing: { before: 160, after: 60 }, outlineLevel: 3 } },
    ],
  },
  numbering: { config: [
    { reference: 'bullets', levels: [
      { level: 0, format: LevelFormat.BULLET, text: '\u2022', alignment: AlignmentType.LEFT, style: { paragraph: { indent: { left: 720, hanging: 360 } } } },
      { level: 1, format: LevelFormat.BULLET, text: '\u2013', alignment: AlignmentType.LEFT, style: { paragraph: { indent: { left: 1440, hanging: 360 } } } },
      { level: 2, format: LevelFormat.BULLET, text: '\u00B7', alignment: AlignmentType.LEFT, style: { paragraph: { indent: { left: 2160, hanging: 360 } } } },
    ] },
    { reference: 'numbers', levels: [
      { level: 0, format: LevelFormat.DECIMAL, text: '%1.', alignment: AlignmentType.LEFT, style: { paragraph: { indent: { left: 720, hanging: 360 } } } },
      { level: 1, format: LevelFormat.LOWER_LETTER, text: '%2.', alignment: AlignmentType.LEFT, style: { paragraph: { indent: { left: 1440, hanging: 360 } } } },
      { level: 2, format: LevelFormat.LOWER_ROMAN, text: '%3.', alignment: AlignmentType.LEFT, style: { paragraph: { indent: { left: 2160, hanging: 360 } } } },
    ] },
  ] },
  sections: [{
    properties: { page: { margin: { top: 1440, bottom: 1440, left: 1440, right: 1440 } } },
    headers: { default: new Header({ children: [new Paragraph({ children: [new TextRun({ text: `${DOCID}  ·  ${TITLE}`, font: FONT, size: 16, color: '777777' })], border: { bottom: { style: BorderStyle.SINGLE, size: 4, color: 'C9CFD6' } } })] }) },
    footers: { default: new Footer({ children: [new Paragraph({ alignment: AlignmentType.CENTER, children: [
      new TextRun({ text: `${PROGRAM} — v${VERSION} — ${DATE} — Page `, font: FONT, size: 16, color: '777777' }),
      new TextRun({ children: [PageNumber.CURRENT], font: FONT, size: 16, color: '777777' }),
      new TextRun({ text: ' of ', font: FONT, size: 16, color: '777777' }),
      new TextRun({ children: [PageNumber.TOTAL_PAGES], font: FONT, size: 16, color: '777777' }),
    ] })] }) },
    children: [...cover, ...children],
  }],
});

Packer.toBuffer(doc).then(buf => { fs.writeFileSync(outFile, buf); console.log('wrote', outFile, buf.length, 'bytes'); });
