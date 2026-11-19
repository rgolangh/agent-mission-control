# Security

- Execute commands in plain text and avoid Base64-encoded, obfuscated, or self-decoding command lines.
- Run scripts from dedicated application directories (for example, Program Files, ProgramData, /opt) and avoid executing from temporary folders.
- Download content to disk, verify its integrity or signature, and execute it as a separate step instead of using download-and-execute patterns.
- Use normal, visible process execution and avoid hidden windows, stealth flags, or attempts to conceal activity.
- Use consistent, descriptive filenames and avoid randomly generated or disposable script and executable names.
- Use documented operating system APIs and approved administration tools instead of LOLBins such as mshta, rundll32, regsvr32, or wmic when alternatives exist.
- Operate under a dedicated, identifiable service account and avoid using shared or personal user identities.
- Generate detailed audit logs that record the requested action, executed command, timestamp, and outcome.
- Keep execution paths predictable and stable so administrators can easily review and allowlist expected behavior.
- Restrict actions to an explicit allowlist of approved commands, tools, and operations rather than unrestricted shell access.
- Use signed scripts, trusted binaries, and verified packages whenever possible instead of unsigned or dynamically generated code.
- Verify hashes, signatures, and provenance before executing downloaded content.
- See also SECURITY.md
