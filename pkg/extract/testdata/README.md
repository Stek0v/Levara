# Synthetic extraction fixtures

Generated locally on 2026-09-05 with reportlab (PDF), python-docx (DOCX),
python-pptx (PPTX), and openpyxl (XLSX). These contain invented test text,
never customer documents. The tests need only Go and the existing parser.

Expected facts: `Revenue is 123`, `Storage` / `35.50`, and Russian text
`Привет, команда` or `Выручка` in Office formats. The PDF has two pages.
These small examples check parsing and content retention, not OCR accuracy,
complex layout fidelity, or model answer quality.
