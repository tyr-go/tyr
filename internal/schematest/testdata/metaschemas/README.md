# Meta-schemas

The official schemas that the tests check the documents of tyr against, kept here so that the tests need no network. Each file is as published, under the name of its `$id`.

| File | `$id` | Source |
|---|---|---|
| `openapi-3.1-schema-base-2026-08-03.json` | `https://spec.openapis.org/oas/3.1/schema-base/2026-08-03` | [OAI/spec.openapis.org](https://github.com/OAI/spec.openapis.org), `oas/3.1/schema-base/2026-08-03` |
| `openapi-3.1-schema-2026-08-03.json` | `https://spec.openapis.org/oas/3.1/schema/2026-08-03` | same, `oas/3.1/schema/2026-08-03` |
| `openapi-3.1-dialect-2024-11-10.json` | `https://spec.openapis.org/oas/3.1/dialect/2024-11-10` | same, `oas/3.1/dialect/2024-11-10` |
| `openapi-3.1-meta-2024-11-10.json` | `https://spec.openapis.org/oas/3.1/meta/2024-11-10` | same, `oas/3.1/meta/2024-11-10` |

The schema-base variant checks the schemas in a document too, against the dialect of OpenAPI 3.1: JSON Schema 2020-12 with the vocabulary of OpenAPI. The meta-schema of JSON Schema 2020-12 itself comes with the validator.

The schemas of the OpenAPI Specification are published by the OpenAPI Initiative under the Apache License 2.0: https://www.apache.org/licenses/LICENSE-2.0.
