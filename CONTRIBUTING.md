# Contributing to YHC

Thanks for improving YHC — Yet Hooked on Coding. The public project lives at
https://github.com/abietic/yhc.

## Before opening an issue or pull request

Use the issue forms and pull-request template. Do not include passwords, API
keys, access tokens, private repository contents, or other secrets in issues,
pull requests, logs, fixtures, screenshots, or commits.

Read the [Code of Conduct](CODE_OF_CONDUCT.md), [security policy](SECURITY.md),
and [contributor guide](docs/contributing/README.md). Security vulnerabilities
belong in the private reporting channel described in SECURITY.md, not in a
public issue.

## Contribution requirements

By submitting a contribution, you affirm that:

1. you created the submitted expression or have the right to submit it;
2. it is compatible with this repository's Apache-2.0 licensing and any
   applicable third-party license or notice obligations;
3. you identify relevant provenance and source mappings truthfully, including
   reference-informed behavior, copied or modified third-party material, and
   required notices; and
4. you have not included secrets, private operational data, or content you are
   not authorized to publish.

There is no separate contributor license agreement. Contributions are accepted
under the terms stated in [LICENSE](LICENSE), subject to any compatible
third-party terms that apply to the material.

## Development and verification

Keep each pull request focused. Preserve unrelated work already in the working
tree. Add or update focused tests for observable behavior, and run the required
local gates before requesting review:

~~~bash
make fmt
make lint
make test
make build
make docs-check
~~~

For source-mapping policy and publication evidence, follow
[docs/publication/README.md](docs/publication/README.md). A source mapping is
evidence, not permission to copy expression; it must remain accurate and must
not replace the rights and provenance affirmations above.
