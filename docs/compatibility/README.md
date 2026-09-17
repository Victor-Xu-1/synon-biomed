# Protocol test fixtures

`scenarios/` contains replayable API and event-contract fixtures. They are test
inputs, not captured user data or a historical completion score.

The contract runner can capture and compare these scenarios against an isolated
loopback test server. Store generated captures outside the source checkout and
pass that directory explicitly as the runner's `--root`.

Historical migration ledgers and captured responses are not source or release
inputs. Current regression tests, release integrity, supply-chain verification
and exact-artifact authorization remain the release checks. The packaged runtime
does not load these test fixtures.
