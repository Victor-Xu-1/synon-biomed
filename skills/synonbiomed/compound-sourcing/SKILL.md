---
name: compound-sourcing
description: Resolve compound identity and find public supplier, catalog, and availability evidence using PubChem plus provider-neutral web search.
license: Apache-2.0
---

# Compound Sourcing

Use this skill when the user asks whether a molecule can be bought, where to
source it, how to identify supplier pages, or how to compare public catalog
signals for a compound. The workflow is identity-first. Resolve the public
chemical identity through the cataloged PubChem MCP method from `repl`:

```python
identity = host.mcp(
    "chemistry",
    "pubchem_search_compounds",
    query="itraconazole",
    namespace="name",
    max_cids=10,
)
```

The result is one object, not a list. Read the bounded response contract as:

- `query`, `namespace`, `n_cids_total`, `truncated`
- `cids[]`
- `properties[]`, where compound fields use PubChem names such as `CID`,
  `MolecularFormula`, `MolecularWeight`, `SMILES`, `ConnectivitySMILES`,
  `InChI`, `InChIKey`, `IUPACName`, `XLogP`, `ExactMass`, and `TPSA`

For example, use `identity["properties"][0]["CID"]` after checking that
`properties` is non-empty. Do not iterate over the top-level object as if each
item were a compound, and do not rename the returned keys to lowercase before
reading them.

Keep the returned CID, title, formula, and SMILES visible, and then use
provider-neutral web search for public supplier and catalog evidence. The Skill
name `compound-sourcing` describes this workflow; it is not an MCP method.
`chemistry/compound_sourcing` is not a cataloged tool and must not be called.

Do not infer availability from memory. A name can map to salts, stereoisomers, or
deprecated aliases, so always keep the identity fields visible when the sourcing
decision matters. If the requested molecule is a designed analog with no exact
catalog hit, say that the result is a similarity or query-based sourcing signal,
not a confirmed purchasable product.

For procurement-facing output, summarize supplier names, source links, catalog
evidence, and unresolved identity issues. Do not fabricate price, stock, purity,
or lead-time fields when the source did not provide them. Keep PubChem identity
resolution and supplier-page search as two explicit evidence stages rather than
inventing a combined sourcing endpoint.
