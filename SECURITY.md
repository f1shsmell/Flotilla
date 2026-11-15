# Security Policy

## Supported Versions

| Version | Security support |
| --- | --- |
| Latest 0.x release (stable channel) | Supported |
| Preview channel | Best effort |
| Older releases, forks, modified builds | Not covered unless reproducible upstream |

## Reporting a Vulnerability

Please report security issues privately through GitHub's
[private vulnerability reporting](https://github.com/f1shsmell/Flotilla/security/advisories/new).
Do not open a public issue with exploit details, secrets, or proof-of-concept
payloads.

Include: affected version/commit, platform, reproduction steps, and impact.
We aim to acknowledge within 72 hours.

## Scope notes

Flotilla's local IPC (named pipes + token) isolates **cross-user** access;
processes running as the same user are inside the trust boundary. The core
never stores vendor credentials — the optional magpie bridge delegates to a
user-installed magpie. Reports against those boundaries are welcome; reports
that assume same-user isolation are out of scope by design.