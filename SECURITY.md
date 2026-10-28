# Security

Please report security vulnerabilities through GitHub's private security
advisory flow for this repository. Include the affected version, the smallest
reproduction you can share safely, and the impact you observed.

Do not open a public issue containing credentials, private repository details,
machine snapshots, or other sensitive data. Remove those values from logs and
examples before attaching them to a report.

The most sensitive boundaries are release acquisition and replacement,
repository locator validation, managed checkout replacement, child-process
environment scoping, bidirectional capture, native restore identity, and
snapshot destination enforcement.

Config verifies the private release adapter against an embedded checksum.
That adapter has no machine configuration and verifies GitHub's release asset
digest. Updates resolve an exact stable version, verify the executable's
version and refuse a downgrade before atomic replacement. Releases are
published by the Release workflow from a tag. Checksums detect corrupt or
mismatched downloads; they do not establish build provenance or protect against
replacement of both an asset and its digest on GitHub.

Machine tools, repositories, skills and MCP configuration are outside Config's
ownership. Native setup remains trusted code; keep credentials and client state
out of tracked declarations and captures. Reports crossing one of these
boundaries are especially useful.
