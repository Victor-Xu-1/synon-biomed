# P2Rank Runtime Notice

Synon Biomed can acquire and run P2Rank as an optional scientific runtime. The
P2Rank archive and Java environment are external runtime state: they are not
committed to this repository and are not silently included in a Synon release.

| Component | Pinned version | Upstream | License |
| --- | ---: | --- | --- |
| P2Rank | 2.5.1 | <https://github.com/rdk/p2rank/releases/tag/2.5.1> | MIT; exact upstream text in `P2RANK-LICENSE.txt` |
| OpenJDK | 17 | `conda-forge` `openjdk=17` | GPL-2.0-only WITH Classpath-exception-2.0 |

The governed download authority for P2Rank is:

- URL: `https://github.com/rdk/p2rank/releases/download/2.5.1/p2rank_2.5.1.tar.gz`
- expected bytes: `275625956`
- SHA-256: `d243f2d9036ac053fefb9407b5fe1c85f4fe077c519fd975ac585e995feab274`
- admitted redirect host: `release-assets.githubusercontent.com`

The runtime verifies the exact archive digest before extraction, rejects links
and unsafe archive members, and records the observed Java major version in the
validation artifact. Redistribution of either runtime component must include
the applicable upstream license text and notices. Generated pocket predictions
and user structures are user data and are not covered by this notice.
