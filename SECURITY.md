# Security policy

## Supported versions

Security fixes are made on `main` and released in a new patch version of the
latest minor release. Older minor versions do not receive fixes; upgrade to
the latest release to pick them up.

| Version              | Supported |
|----------------------|-----------|
| latest minor (0.x.y) | yes       |
| older minors         | no        |

## Reporting a vulnerability

Please **do not** open a public issue, pull request or discussion for a
security problem.

Report it privately through GitHub's private vulnerability reporting:
open the repository's
[Security tab](https://github.com/infrabay/peopleforce-cli/security)
and choose **Report a vulnerability**
([direct link](https://github.com/infrabay/peopleforce-cli/security/advisories/new)).

Please include:

- the `peopleforce version` output and your operating system;
- a description of the issue and its impact;
- the exact command line and, where relevant, a minimal API response that
  reproduces it, with API keys and employee data removed.

Reports are acknowledged as soon as possible. Once the issue is confirmed,
a fix is prepared in a private security advisory and released, and the
advisory is published with the fixed version, crediting the reporter unless
they prefer otherwise.

## Scope

This policy covers the code in this repository: the `peopleforce` binary and
its release artifacts. The CLI handles a PeopleForce API key and HR personal
data, so issues such as these are in scope:

- the API key reaching a log, an error message, argv, another host (for
  example across a redirect) or a file with loose permissions;
- request data sent somewhere other than the configured API URL;
- a destructive operation running without `--yes`;
- API response data that can inject terminal control sequences or write
  outside the intended path.

Vulnerabilities in the PeopleForce service or its API should be reported to
PeopleForce. Vulnerabilities in a dependency should be reported to that
project; if one affects this CLI, a report here is still welcome so the
dependency can be updated.
