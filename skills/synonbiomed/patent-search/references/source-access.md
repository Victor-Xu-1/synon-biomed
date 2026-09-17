# Patent source access contract

Use this matrix to keep discovery, official evidence, and downloads distinct.

| Source | Best use | Machine boundary | Download rule |
| --- | --- | --- | --- |
| WIPO PATENTSCOPE | PCT publications, families, multilingual full text | Public portal; some bulk or service paths have separate terms | Open the official record and use its PDF or ZIP document action. Treat PDF as legally reliable and OCR or HTML text as discovery text. |
| Google Patents | Broad discovery, familiar search syntax, family navigation | Public web interface; no official public product API is assumed | Use only a PDF URL exposed by the selected publication page and accepted by the tool's HTTPS host allowlist. Verify against an official office record. |
| EPO Espacenet / OPS | EP and worldwide bibliographic, legal, full-text, and image data | Espacenet is interactive. OPS is REST/XML with registration, OAuth credentials, quota, and terms | Use Espacenet interactively, or a separately configured OPS integration. Never place OPS secrets in a prompt or artifact. |
| CNIPA PSS | Chinese patent search, analysis, and publication downloads | The official portal may require free registration, sign-in, and CAPTCHA | The user completes the access gate. Search the exact publication number and use the official download action; do not scrape around the gate. |

## Query construction

- Combine exact names with synonyms and abbreviations.
- For medicinal chemistry, include target, mechanism, scaffold or Markush class, substituent language, activity endpoint, and assignee when known.
- For biologics, include sequence identifiers, target or epitope, modality, construct terms, and applicant.
- Search Chinese and English equivalents for CNIPA work, then compare publication numbers and priority families rather than assuming translated titles are unique.

## Evidence labels

- `discovered`: search result only; not yet read as a patent record.
- `official-record-location`: deterministic source link for the normalized publication number.
- `resolved`: a trusted HTTPS PDF URL was extracted from the selected record page.
- `user_action_required`: an official portal requires interactive navigation, registration, authentication, or CAPTCHA.
- `unavailable`: the source did not expose a trusted download URL or could not be read; do not invent one.

## Safety

Never bulk-download beyond the source's terms, bypass robot or account controls, submit credentials supplied in chat, or call a search result legal advice. Preserve the exact publication number and source provenance for every saved file.
