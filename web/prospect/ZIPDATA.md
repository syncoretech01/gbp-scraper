# uszips.csv.gz — embedded US ZIP dataset

`uszips.csv.gz` is the gzipped CSV embedded into the binary by
`zipdata.go` and exposed through `EmbeddedZIPDataset` /
`EmbeddedZIPAreas` / `EmbeddedZIPStates`. It is the production
fallback used by query generation when no ZIP CSV is uploaded
(`SampleZIPAreas` remains only as a tiny fixture).

## Contents

- Header: `zip,city,state,latitude,longitude,population` — exactly the
  header `ParseZIPCSV` expects.
- 40,979 data rows, one per unique 5-digit ZIP code.
- 52 state/territory codes, including DC.
- 33,050 rows carry a 2020+ US Census ACS ZCTA population. A blank
  population means "unknown" and parses as 0.
- Latitude/longitude are ZIP centroids rounded to 4 decimal places.

## Sources and licenses

| Source | What it provides | License |
| --- | --- | --- |
| [GeoNames postal dataset](https://download.geonames.org/export/zip/) (US.zip) | ZIP, city, state, centroid coordinates | [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/) — attribution required; this file is that attribution |
| US Census Bureau ACS ZCTA population estimates | Population per ZCTA | Public domain (US government work) |

Merged on 2026-08-20.

## Regeneration recipe

1. Download `US.zip` from <https://download.geonames.org/export/zip/>
   and extract `US.txt` (tab-separated: country, postal code, place
   name, admin1 name, admin1 code, ..., latitude, longitude, accuracy).
2. Download the latest ACS 5-year ZCTA total-population table
   (`B01003`) from the US Census Bureau API or data.census.gov, keyed
   by 5-digit ZCTA.
3. Merge on the 5-digit ZIP: take ZIP, place name, admin1 code
   (state), latitude and longitude from GeoNames; attach the ACS
   population where the ZIP matches a ZCTA, otherwise leave the
   population blank (blank = unknown = 0).
4. Keep only rows whose ZIP is exactly 5 ASCII digits; deduplicate by
   ZIP keeping the first occurrence; round coordinates to 4 decimals.
5. Write the CSV with the header
   `zip,city,state,latitude,longitude,population`, sorted by ZIP, and
   gzip it as `uszips.csv.gz`.
6. Sanity-check before committing: the row count stays near 41k, every
   row passes `ParseZIPCSV`, and `TestEmbeddedZIPDataset` in
   `zipdata_test.go` still passes.
