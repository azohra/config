# Security

Please report security vulnerabilities through GitHub's private security
advisory flow for this repository. Include the affected version, the smallest
reproduction you can share safely, and the impact you observed.

Do not open a public issue containing credentials, private repository details,
machine snapshots, or other sensitive data. Remove those values from logs and
examples before attaching them to a report.

The most sensitive boundaries are release acquisition and replacement, the
pinned Mise download, agent-skill acquisition, repository locator validation,
managed checkout replacement, child-process environment scoping, bidirectional
capture, and snapshot destination enforcement. Agent-skill sources are trusted
code and instructions: Config invokes an exact skills CLI package from its own
npm cache, accepts only repository locators, and preserves installed content
that no longer matches its ownership digest until Apply explicitly adopts a
compatible same-source change. Config verifies the downloaded
Mise bytes against the checksum embedded for its tested release before
replacing the standalone command. Released updates use a separate cache-owned
Mise adapter, not the machine resource, with GitHub asset digest verification.
They resolve an exact stable version, verify the executable's version and refuse
a downgrade before atomically replacing the permanent command. Releases can be
built and published locally or through the Release workflow. Checksums detect
corrupt or mismatched downloads; they do not establish build provenance or
protect against replacement of both an asset and its digest on GitHub.
Reports that cross one of those boundaries are especially useful.
