# Data-analysis workflow

1. Inventory datasets, periods, timezones, currencies, units, missing values, duplicates, and join keys.
2. Store formulas and filters in criterion verification metadata.
3. Build a reproducible extract → normalize → validate → join → calculate → report pipeline.
4. Record checksums and data-quality outputs as receipts.
5. Do not infer missing join keys or values without an explicit documented rule.
6. Do not claim causality from correlation.
7. Re-run the pipeline after every transformation change and use the new revision in receipts.
8. Require independent calculation review for financial or high-impact metrics.
