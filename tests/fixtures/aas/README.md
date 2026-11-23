# Vendored AAS V3.0 JSON schema

`aas-v3.0.6.json` is the official JSON schema of the IDTA Asset Administration
Shell Part 1 metamodel, version 3.0.6, unmodified.

- Source: <https://raw.githubusercontent.com/admin-shell-io/aas-specs/v3.0.6/schemas/json/aas.json>
- SHA-256: `4baf8c108ee3aa736408e2a5aa6bfd31097ced447fa59716354f1213ccca115a`
- Copyright: Industrial Digital Twin Association (IDTA) and the aas-specs contributors
- License: [Creative Commons Attribution 4.0 International (CC BY 4.0)](https://creativecommons.org/licenses/by/4.0/)

It is a test fixture only: `tests/test_aas_export.py` validates Hades' AAS
exports and the AAS examples against it (Hades-045). It is not shipped in a
release. Go's RE2 regular expressions cannot compile the schema's surrogate
escapes, so Python `jsonschema` (Draft 2019-09) is the validator.
