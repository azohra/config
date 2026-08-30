# Security

Please report security vulnerabilities through GitHub's private security
advisory flow for this repository. Include the affected version, the smallest
reproduction you can share safely, and the impact you observed.

Do not open a public issue containing credentials, private repository details,
machine snapshots, or other sensitive data. Remove those values from logs and
examples before attaching them to a report.

The most sensitive boundaries are release acquisition and replacement,
repository locator validation, managed checkout replacement, child-process
environment scoping, bidirectional capture, and snapshot destination
enforcement. Released updates use Config's canonical mise binary with GitHub
artifact attestation enabled, resolve an exact stable version, and refuse a
downgrade before atomically replacing the permanent command. Reports that cross
one of those boundaries are especially useful.
