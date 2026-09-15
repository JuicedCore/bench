# Final runs

Curated archives of finished benchmark campaigns, copied out of `results/` so they
survive `make clean`. One directory per campaign, named `<date>-<profile>`. Runs
are comparable only within the same profile.

| Campaign | Profile | Platforms | Configs | Outcome |
| -------- | ------- | --------- | ------- | ------- |
| [2026-09-14-local-small](2026-09-14-local-small/README.md) | local-small | fabric-cft, fabric-bft, drunix, fabricx | quick-smoke, probe-sweep | smoke 4/4 ok; knees measured on all four; headline only on fabric-bft (others lost a container past the knee) |

Each campaign directory has a `README.md` covering:
- the command
- the headline table
- caveats and failures with evidence
- the file layout

How to produce and read these: [README → Running every combination](../README.md#running-every-combination)
and [README → Results](../README.md#12-results-where-they-are-and-how-to-read-them).
