# Security policy

## Reporting a vulnerability

Use GitHub's **Security** tab and choose **Report a vulnerability**. This creates
a private security advisory that can be discussed without exposing users or
providing an exploit recipe publicly.

Do not open a public issue containing credentials, private keys, authentication
tokens, personal data, infrastructure details, or a working exploit. If private
reporting is temporarily unavailable, open a minimal issue asking the maintainer
to enable a private channel and omit all sensitive details.

Include the affected component, impact, reproduction prerequisites, and a
suggested mitigation when available. Reports are assessed against the latest
commit on `main`. Do not copy, use, or validate an accidentally exposed secret;
report only its repository location. Disclosure should remain coordinated until
a fix is available. This project does not currently promise a bug bounty.

## Scope

This public repository contains portable source code and no production
deployment credentials or infrastructure configuration. A public finding must
never be tested against infrastructure you do not own or have explicit
permission to assess. Scanning a live demo, origin server, wallet, funded
contract, or cloud account is outside scope, as are denial-of-service tests.
