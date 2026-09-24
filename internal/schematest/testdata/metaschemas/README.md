# Meta-schemas

The official schemas that the tests check the documents of tyr against, kept here so that the tests need no network. Each file is as published; the table gives its `$id` and where it comes from.

| File | `$id` | Source |
|---|---|---|
| `openapi-3.1-schema-base-2026-08-03.json` | `https://spec.openapis.org/oas/3.1/schema-base/2026-08-03` | [OAI/spec.openapis.org](https://github.com/OAI/spec.openapis.org), `oas/3.1/schema-base/2026-08-03` |
| `openapi-3.1-schema-2026-08-03.json` | `https://spec.openapis.org/oas/3.1/schema/2026-08-03` | same, `oas/3.1/schema/2026-08-03` |
| `openapi-3.1-dialect-2024-11-10.json` | `https://spec.openapis.org/oas/3.1/dialect/2024-11-10` | same, `oas/3.1/dialect/2024-11-10` |
| `openapi-3.1-meta-2024-11-10.json` | `https://spec.openapis.org/oas/3.1/meta/2024-11-10` | same, `oas/3.1/meta/2024-11-10` |
| `openrpc-1.4.1-schema.json` | `https://meta.open-rpc.org/` | [open-rpc/spec](https://github.com/open-rpc/spec), `spec/1.4/schema.json` at tag v1.4.1 |
| `openrpc-json-schema-tools-meta.json` | `https://meta.json-schema.tools/` | [json-schema-tools/meta-schema](https://github.com/json-schema-tools/meta-schema), `src/schema.json`, as https://meta.json-schema.tools/ serves it |

The schema-base variant checks the schemas in a document too, against the dialect of OpenAPI 3.1: JSON Schema 2020-12 with the vocabulary of OpenAPI. The meta-schema of JSON Schema 2020-12 itself comes with the validator.

The OpenRPC schema describes its schemas by the second file, a Draft 7 meta-schema that names itself as its `$schema`. The tests read both as Draft 7 and resolve the reference of the first to the second, which lacks the slash of its `$id`.

The schemas of the OpenAPI Specification are published by the OpenAPI Initiative, those of the OpenRPC Specification by the OpenRPC project, and the meta-schema of json-schema-tools by its authors, all under the Apache License 2.0: https://www.apache.org/licenses/LICENSE-2.0.
