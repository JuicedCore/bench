# Fabric-X: comparability analysis and current status

## What was asked

Fabric-X and NeuChain were the only two platforms with just a single
`quick-smoke` run config, unlike fabric-cft/fabric-bft/drunix's full set
(`probe-sweep`, `throughput-scan`, `latency-profile`, `contention`,
`multi-client`). The plan: add the missing five for Fabric-X (using its one
working operation, `transfer`, since `kv-write`/`kv-read` are separately
blocked by an unfinished view service), live-verify them, and answer directly
whether a Fabric-X `transfer` run's numbers are comparable to the
Fabric-family's `kv-write`/`transfer` numbers.

**That plan changed when the live test failed before producing any numbers at
all** — and a deeper investigation (re-analyzed independently after an initial
too-quick "unfixable" conclusion was challenged) found the real story is more
precise, partially fixable, and partially a dead end. This document reports
what was actually found and fixed, not the original plan.

## Status: root-caused precisely, two real bugs fixed, one dead end confirmed by testing

### Bug 1 (fixed) — `deploy/docker/fabricx/up.sh` never re-cloned on `FXS_REF` change

`[ -d "$FXS/.git" ] || git clone ...` only ever cloned once into the
persistent `.cache/`. Changing `FXS_REF` on a machine with an existing clone
silently did nothing, and `BENCH_PLATFORM_VERSION=fabric-x-samples-${FXS_REF}`
could record a ref that was never actually checked out. Fixed: `up.sh` now
compares the currently checked-out ref against `$FXS_REF` and re-clones on
mismatch.

### Bug 2 (fixed) — `endorser/init`'s failure was silently swallowed

The original `curl -sf -X POST .../endorser/init || warn "...may already be
initialised"` discarded the response body (`-sf`) and downgraded a hard
failure to a warning, then `up.sh` proceeded to write `connection.env` and
declare success over a dead stack. Fixed to mirror upstream's own
`tokens/scripts/test.sh` `init_fabricx()`: wait for `/readyz` first, retry
with a real backoff (30 attempts, 5s apart), print the actual response body on
every failure, and `die` for real if all retries are exhausted.

### Bug 3 (fixed) — `tokens/`'s own inventory was missing required base-path variables

Found while re-testing after Bug 2's fix surfaced a *new*, earlier failure:
`setup-fabric` failing with `'config_build_dir' is undefined` in the
`cryptogen` role, before ever reaching `endorser/init`. Traced exhaustively
(grepped the entire `ansible/` tree): `config_build_dir` and its dependency
chain (`project_dir` → `out_dir` → `control_node_dir` →
`{cryptogen,configtxgen,armageddon}_artifacts_dir`, plus `channel_id`/
`actual_host`) are defined **only** in the `hyperledger.fabricx` collection's
own `examples/inventory/vars.yaml` — a file `fabric-x-samples`' `tokens/`
sample never copies into its actual inventory (`tokens/ansible/inventory/`
ships only `fabric-x.yaml` + `group_vars/all/env.yaml`). Confirmed these vars
are load-bearing, not example-only cruft: referenced unconditionally across
dozens of role files (`fetched_artifacts_dir` alone in 42). This is a genuine
gap in `fabric-x-samples`' own shipped inventory, not something we
misconfigured — isolated by removing an unrelated change and reproducing the
identical failure.

**Fixed**: `up.sh` now writes the missing base-path vars into
`tokens/ansible/inventory/group_vars/all/vars.yaml` every run (adapting
`project_dir` to the same `PROJECT_DIR` env var `fabricx_ansible.mk` already
exports, so `out_dir` lands at `tokens/out` — the directory this setup already
treats as its scratch space elsewhere). **Verified**: a fresh `up.sh` run after
this fix brought the full ~21-container devnet up cleanly with zero setup
errors — `setup-fabric` had failed on every attempt before this fix, succeeded
on every attempt after it.

### The real root cause (confirmed, unchanged by the above) — committer version skew

With bugs 1-3 fixed, `up.sh` now reliably reaches the actual blocker every
time, cleanly:

```
$ curl -X POST http://localhost:9300/endorser/init
failed getting state: get states: query get rows: rpc error: code = Unimplemented
desc = unknown service committerpb.QueryService
```

Traced through source (not inferred from behavior):

1. `tokens/endorser/service/fsc.go`'s `Init()` calls into `panurus`'
   `PublicParametersService.Fetch()`, which calls FSC's query-service client
   (`fabric-smart-client/platform/fabricx/core/committer/queryservice/query.go`),
   whose gRPC client is typed `committerpb.QueryServiceClient`
   (`github.com/hyperledger/fabric-x-common/api/committerpb`).
2. `tokens/go.mod` pins `fabric-smart-client v0.18.0` → `fabric-x-common
   v0.2.8`; `go.sum` separately pins `fabric-x-committer v1.0.4`.
3. The samples repo's own Ansible role (`hyperledger.fabricx` collection
   v0.5.5, pinned in `tokens/ansible/requirements.yml`) deploys
   `fabric-x-committer:0.1.7` — three major versions behind what the built
   endorser app's dependency graph expects. Stale doc comments in FSC's own
   source (`query.go`) still reference `protoqueryservice.Query`/`Rows` — the
   pre-rename type names — corroborating that the RPC was renamed at some
   point after 0.1.7.
4. This is a genuine mismatch **inside `fabric-x-samples` itself**, between
   its own bundled app code and its own infrastructure pin. Two alternative
   theories were checked and refuted by reading source directly: it is not a
   wrong endpoint/port (the endorser's `core.yaml` correctly points
   `queryService` at `committer-query-service:7001`, and FSC's gRPC client
   code has no fallback path to any other port), and it is not a missing
   setup/init step (the namespace-create/list bootstrap the collection's own
   `60-start.yaml` playbook runs completes successfully before `endorser/init`
   is ever called).

### Lever B, tried and confirmed to fail

Since `committer_image_tag` is a low-precedence Ansible role default, it's
trivially overridable. Tried overriding it to `1.0.4` to match `go.sum`.
**Result: every committer container (coordinator, sidecar, validator,
verifier, query-service) crash-loops immediately:**

```
Error: unknown command "committer" for "Committer"
```

This is worse than the 0.1.7 baseline (which at least runs, just fails one
RPC) — the 1.0.4 binary's CLI interface was restructured, not just its
internal service names or config schema. The collection's own role templates
(written for 0.1.7's CLI/config) are simply incompatible with 1.0.4. **The tag
override has been reverted** — `up.sh` documents this as a tried-and-failed
dead end rather than silently dropping it, so nobody re-attempts the same
naive fix.

### Lever C: self-built backend — root cause fixed and confirmed, new blocker found during integration

Since Lever B failed, built a replacement backend from source instead of
patching the Ansible-deployed one: `deploy/docker/fabricx/backend/` (`Dockerfile`,
`run.sh`, `generate-arma.sh`, `arma-deployment.yaml`, `patch-sidecar-identity.py`).
It compiles `fabric-x-committer` and `fabric-x-orderer` from source at fixed
commits into one Docker image (`bench/fabricx-backend:latest`) that runs a
4-party/1-shard Arma network plus all five committer services (sidecar,
coordinator, verifier, vc, query) in a single container, and `up.sh` now builds
and starts it in place of the Ansible-deployed orderer+committer (see the
`docker rm -f orderer-* committer-*` / `docker build .../backend/Dockerfile`
block in `up.sh`).

**Pinned source commits** (both cloned into `deploy/docker/fabricx/.cache/`,
not part of this repo):
- `fabric-x-committer` @ `7ca0d43148e90df697b8b3a2cd0b1cd4db6d25a0` (`.cache/fabric-x-committer-src`)
- `fabric-x-orderer` @ `5796962454f3589368740f717f81b942c4d80471` (`.cache/fabric-x-orderer-src`)
- `hyperledger/fabric-x` @ tag `v1.0.1` (`.cache/fabric-x-tools-src`) — provides `tools/fxconfig`, built from source because the `hyperledger/fabric-x-tools:0.0.8` image hit the identical class of bug from the other direction (below)

**Root-cause fix confirmed**: `docker logs fabricx-backend` shows
`committerpb.RegisterQueryServiceServer(s.GRPC, q)` firing
(`service/query/query_service.go:124` in the committer source) and the
gRPC server listening on `:7001`; Arma's 4 consensus/batcher/assembler/router
processes exchange BFT messages and append blocks; the sidecar delivers block 0
on `channel arma` (the same channel name `tokens/`'s `core.yaml` already
expects) and connects to the coordinator. None of this exists in the
Ansible-deployed 0.1.7 baseline. This part of the fix is done.

**New blocker, found while making the network actually usable — a namespace
must exist before any application transaction can run.** Sequence of what was
tried and what happened, in order, so debugging can start from the actual
evidence rather than redo this sequence:

1. `fxconfig namespace create token_namespace --channel arma --orderer
   committer-sidecar:6022 --mspConfigPath <path> --mspID Org1MSP --pk
   .../token_namespace/pubkey.pem`, using the `hyperledger/fabric-x-tools:0.0.8`
   image and `tokens/`'s own already-generated
   `out/local-deployment/orderer-loadgen/config/fxconfig/` material (identity:
   `Org1MSP`, from `out/.../config/users/channel_admin@org1.example.com/msp`).
   Broadcast succeeded (no client-side error), but the DB never gained a
   `ns_token_namespace` table, and `tx_status` in the committer's Postgres
   (`docker exec fabricx-backend psql -h 127.0.0.1 -p 5433 -U postgres -d
   postgres -c "SELECT * FROM tx_status;"`) showed `status = 104` for the tx.
   `104` is `Status_MALFORMED_BAD_ENVELOPE_PAYLOAD` per
   `fabric-x-common@v0.2.8/api/committerpb/status.pb.go:48` (module cache path:
   `~/go/pkg/mod/github.com/hyperledger/fabric-x-common@v0.2.8/`) — same status
   with two other identities tried (`org1`/`Admin@org1`, from
   `/root/arma/crypto/ordererOrganizations/org1/users/...` inside the
   `fabricx-backend` container).
2. Built `fxconfig` from source instead (see pinned commits above — matches
   `tokens/go.mod`'s own `fabric-x-committer v1.0.4` / `fabric-x-common v0.2.8`
   pins exactly). Its CLI is a different shape entirely: `fxconfig namespace
   create <name> --policy="<DSL>" --endorse --submit --wait --config=<yaml>`
   (schema and DSL syntax: `tools/fxconfig/docs/README.md` in the
   `fabric-x-tools-src` clone). Re-ran with a config pointing `orderer.address`
   at `committer-sidecar:6022`, `queries.address` at
   `committer-query-service:7001`, `tls.enabled: false` throughout (matching
   `tokens/`'s own `core.yaml`: `tls.enabled: false`).
3. With the source-built `fxconfig`, the envelope parses (no more `104`/
   `MALFORMED_BAD_ENVELOPE_PAYLOAD`). Instead: `Transaction status:
   ABORTED_SIGNATURE_INVALID`, with every identity tried:
   - `msp.localMspID: org1`, MSP dir `/root/arma/crypto/ordererOrganizations/org1/users/client@org1/msp` (copied out via `docker cp`), `--policy="OR('org1.member')"`
   - `msp.localMspID: OrdererOrg1`, same MSP dir, `--policy="OR('OrdererOrg1.member')"` — `OrdererOrg1` is the MSPID `fabric-x-orderer-src/testutil/fabric/sampleconfig/configtx.yaml:323` (`ConsenterMapping`, party 1) assigns to this party
   - `msp.localMspID: SampleOrg`, MSP dir = the static, checked-in
     `fabric-x-orderer-src/testutil/fabric/sampleconfig/msp/` (not
     per-run-generated), `--policy="OR('SampleOrg.member')"` — `SampleOrg` is
     the Application-section org in the `SampleFabricX` profile
     (`configtx.yaml:656-671`, the profile whose `OrdererType: arma` matches
     what `armageddon generate` actually builds; confirmed via
     `docker logs fabricx-backend | grep "orderer type: arma"`)
   - For this last identity, independently verified the checked-in cert and
     key are a matching pair: `openssl x509 -in signcerts/peer.pem -pubkey
     -noout` vs `openssl pkey -in keystore/key.pem -pubout` produced identical
     output.
4. No working example of `fxconfig namespace create` (or any namespace
   bootstrap) against an `armageddon`-generated genesis was found in either
   repo's own test suites: `fabric-x-tools-src/tools/fxconfig/integration/*_test.go`
   exercises namespace creation only against a separate, `cryptogen`-generated
   fixture (`integration/testdata/`), not an Arma genesis; `fabric-x-orderer`'s
   `test/basic` suite doesn't touch namespaces at all (namespace/policy
   handling lives in the committer, not the orderer).

**Where to look next, for whoever picks this up**: the signature-verification
path for a namespace-create transaction, from the point it's validated against
the channel's policy — start at
`fabric-x-committer-src/service/verifier/policy/policy.go` and
`service/vc/preparer.go` (both surfaced by grepping the committer source for
`namespace`/`policy`; see also `service/sidecar/mapping.go` and
`loadgen/workload/namespace.go`, the committer's own first-party namespace-tx
builder, which is a live comparison point since it's known to work in the
committer's own test suite). The three identities above were chosen based on
reading `configtx.yaml`'s org/profile definitions directly, not guessed
blindly — the config file that assigns each candidate MSPID to a role is cited
next to each one above so that mapping can be re-verified independently.

## What this means for comparability, right now

**Still no Fabric-X comparability numbers — but the path to getting them is
now precisely scoped, not open-ended.** Fabric-X's own setup is reliable now
(bugs 1-3 fixed); the version-skew root cause is fixed and proven (Lever C);
one remaining, well-evidenced blocker (namespace bootstrap, above) stands
between here and a working `endorser/init`. The five new configs remain
written and ready but unverified:
`configs/native/{contention,probe-sweep,throughput-scan,latency-profile,multi-client}-fabricx.yaml`,
plus the pre-existing `configs/native/quick-smoke-fabricx.yaml` — none runnable until
the version-skew is actually resolved (not just tag-bumped).

## The comparability question, answered in principle (pending real numbers)

- **Not directly comparable to `kv-write`/`kv-mixed` on any platform.**
  Fabric-X's `transfer` and the Fabric-family's chaincode `kv-write`/`Put` are
  different operation shapes (native token transfer over REST vs. a chaincode
  invoke), different consensus (Arma vs. Raft/SmartBFT), and `transfer` is
  already marked `normalized: false` — explicitly outside the normalized
  bucket used for fabric-cft/fabric-bft/drunix's apples-to-apples numbers.
- **Potentially comparable to the Fabric-family's own `transfer` numbers**
  (from `contention.yaml`, which uses the `kvstore` chaincode's `Transfer`
  function) as a separate, narrower fair-comparison lane: both are "move value
  read-modify-write between two accounts under contention," even though the
  underlying mechanics differ. Still can't be filled in until Fabric-X
  actually runs a transaction.
- **What it would take to close the `kv-write` gap specifically**: finish the
  `kvview` FSC view service (`deploy/docker/fabricx/kvview/`, currently a `501`
  stub) — itself gated on having a working devnet to implement against.

## What would actually unblock the version skew

Confirmed a naive tag bump doesn't work — the fix needs to bring templates and
binary into agreement, not just swap an image tag. Three options were
considered; the third (Lever C) is implemented and is the current path:

1. **Bump the whole `hyperledger.fabricx` collection**, not just the image
   tag — a newer collection release should ship role templates/CLI-flag
   invocations matching whichever committer version it defaults to. Not
   attempted (network access to Ansible Galaxy to survey versions wasn't
   used); Lever C made this unnecessary.
2. **Pin `tokens/`'s `go.mod` back** to a commit predating the
   `protoqueryservice` → `committerpb` rename, matching `fabric-x-committer
   0.1.7`'s actual API. Not attempted for the same reason.
3. **Build committer + orderer from source ourselves (Lever C, implemented)**:
   `deploy/docker/fabricx/backend/`, described above. This is the current
   state of the repo — confirmed to fix the version-skew RPC issue, blocked on
   the namespace-bootstrap signature-validation issue described above.

Until the namespace-bootstrap issue above is resolved, treat Fabric-X as:
setup reliable (Bugs 1-3), version-skew root cause fixed and proven (Lever C),
blocked on one remaining, well-evidenced integration step (namespace
bootstrap) — pick up directly from the "Where to look next" pointers above
rather than re-deriving the investigation.

## Related reading

- [`COMMANDS.md`](../../COMMANDS.md) — Fabric-X's entry, updated with this finding.
- [`CONFIGS.md`](../../CONFIGS.md) — the compatibility matrix; Fabric-X's new configs are listed but flagged unverified.
- [`docs/REMAINING-WORK.md`](../REMAINING-WORK.md) — the other two platform-level gates (NeuChain compute, Drunix write-path — resolved).
