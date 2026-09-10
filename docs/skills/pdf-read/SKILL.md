---
description: Extract and comprehend PDF files (including long 100+ page documents) by an LLM, covering parsing, chunking, retrieval, and reasoning strategies.
---

# PDF Read Skill

## When to use

Apply this skill when the user wants an LLM to read, understand, answer questions about, or summarize a PDF file. This is especially relevant for long documents (e.g., 100 pages) that exceed a single LLM context window.

Do **not** use this skill for non-PDF files (Word, HTML, plain text) — those have their own simpler extraction paths. Do not use it when the user only wants to download or store a PDF without reading it.

## Workflow

### 1. Inspect the PDF

Before choosing a parsing strategy, determine:

- **Page count** — `pdfinfo` or `PyMuPDF` (`fitz`). If > ~30 pages, plan for chunking or retrieval.
- **Text vs. scanned** — try extracting text from page 1. If empty or garbled, the PDF is scanned/image-based and needs OCR.
- **Layout complexity** — single-column prose is easy; multi-column, tables, headers/footers, and footnotes need layout-aware parsers.

### 2. Parse / extract content

Choose a parser based on the PDF type:

| PDF type | Recommended tool | Notes |
|---|---|---|
| Simple text PDF | `PyMuPDF` (fitz) | Fast, good text extraction, preserves page boundaries |
| Tables + forms | `pdfplumber` | Excellent table detection, works on text PDFs |
| Complex layout (multi-column, academic papers) | `Marker` or `unstructured.io` | Layout-aware, produces structured Markdown |
| Scanned / image PDF | `PaddleOCR` or `Tesseract` via `pytesseract` | OCR required; `unstructured.io` wraps this |
| Mixed (text + images) | `unstructured.io` or `Marker` | Handles both natively |

**Key rule:** Always preserve **page numbers** and **section headings** as metadata. The LLM needs to cite sources and understand document structure.

### 3. Chunk the document

For a 100-page PDF, you cannot feed everything into one LLM call. Chunk strategically:

- **Prefer semantic chunking** — split on headings/sections, not arbitrary character counts. Each chunk should be a coherent unit.
- **Target chunk size:** 500–1500 tokens per chunk. Smaller chunks retrieve more precisely; larger chunks preserve more context.
- **Overlap:** 10–20% overlap between adjacent chunks to avoid splitting ideas mid-sentence.
- **Preserve metadata** on every chunk: `{page_start, page_end, section_title, document_name, chunk_index}`.

If the document has a clear table of contents or heading hierarchy, use it to drive chunk boundaries.

### 4. Embed and store (for retrieval)

When the user will ask multiple questions or the document is long:

- Generate embeddings for each chunk (e.g., `text-embedding-3-small`, `BAAI/bge-small-en`, or similar).
- Store chunks + embeddings + metadata in a vector store (FAISS for local, Chroma for simplicity, or a managed service).
- At query time, retrieve the top-k most relevant chunks (k=5–10 is typical) and feed them to the LLM as context.

For a single one-shot question on a 100-page PDF, you may skip embedding and instead use a **map-reduce** approach: summarize each section, then combine summaries.

### 5. Feed to the LLM

Two main patterns:

**Pattern A — RAG (retrieval-augmented generation):**
Best for Q&A, fact-finding, "what does the document say about X?"
1. Embed the user's query.
2. Retrieve top-k relevant chunks.
3. Inject chunks into the prompt with clear source labels.
4. Ask the LLM to answer using only the provided context.

**Pattern B — Map-reduce summarization:**
Best for "summarize the whole document" or holistic understanding.
1. Summarize each chunk independently (map step).
2. Combine chunk summaries into a final summary (reduce step).
3. If the combined summaries are still too long, recurse.

**Pattern C — Long-context LLM (if available):**
If using an LLM with a very large context window (e.g., 100k+ tokens), you may be able to feed the entire extracted text directly. Verify the token count first — 100 pages of dense text can be 50k–100k tokens.

### 6. Prompt engineering for document understanding

When constructing the LLM prompt:

- **System role:** Tell the LLM it is reading a document and should answer based only on the provided text.
- **Cite sources:** Instruct the LLM to reference page numbers or section titles from the metadata.
- **Handle uncertainty:** Tell the LLM to say "not found in the document" rather than hallucinate.
- **Structure the context:** Label each chunk with `[Page X–Y, Section: Title]` so the LLM can reason about location.

## Constraints

- **Do not** send an entire 100-page PDF raw to an LLM without checking token limits. Most LLMs have context windows of 4k–128k tokens; 100 pages can exceed this.
- **Do not** lose page numbers. The user will want to know where answers came from.
- **Do not** use OCR unless necessary — it is slow and error-prone. Check if the PDF has a text layer first.
- **Do not** chunk mid-sentence or mid-paragraph if avoidable. Semantic boundaries matter.
- **Do not** assume the LLM can reason across 100 pages in one shot. Use retrieval or summarization chains.
- **Respect file size limits** — if the PDF is hundreds of MB, warn the user that processing will be slow.
- **Do not** upload the PDF to external services unless the user explicitly approves (privacy/security boundary).

## Validation

After running the workflow, confirm:

1. Text was successfully extracted from every page (no silent failures on specific pages).
2. Page numbers and section headings are preserved in the chunk metadata.
3. The LLM's answer includes citations to specific pages or sections.
4. For Q&A: the answer is grounded in the document, not hallucinated.
5. For summarization: the summary covers the full document scope, not just the first few pages.
6. If OCR was used, spot-check a few pages for garbled text.

If any page fails extraction, report it to the user rather than silently dropping content.
