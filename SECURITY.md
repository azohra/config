# Security

Please report security vulnerabilities through GitHub's private security
advisory flow for this repository. Include the affected version, the smallest
reproduction you can share safely, and the impact you observed.

Do not open a public issue containing credentials, private repository details,
machine snapshots, or other sensitive data. Remove those values from logs and
examples before attaching them to a report.

The most sensitive boundaries are release acquisition and replacement, the
pinned Mise download, repository locator validation, managed checkout
replacement, child-process environment scoping, bidirectional capture, and
snapshot destination enforcement. Config verifies the downloaded Mise bytes
against the checksum embedded for its tested release before replacing the
standalone command. Released updates use a separate cache-owned Mise adapter,
not the machine resource, with GitHub artifact attestation and SLSA verification
pinned rather than inherited. They resolve an exact stable version and refuse a
downgrade before atomically replacing the permanent command. Releases are built
and published by separate workflow jobs, and only the publishing one holds
write or signing credentials; publishing outside that workflow is refused.
Reports that cross one of those boundaries are especially useful.
