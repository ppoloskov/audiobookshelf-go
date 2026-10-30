# Strict-ledger diagnostic classification

Baseline: upstream `edb1f7efc92ecacdef51aafb4d9d5054ce10604f`. This audit classifies validator output only; it does not upgrade capability status and does not interpret absent evidence as a behavioral mismatch.

Reproduce from the repository root:

```sh
python3 go-backend/tools/parity/classify-ledger-findings.py > go-backend/contracts/parity/strict-ledger-findings.json
```

The script runs `check-ledger.go -require-verified -require-invariants`, preserves each diagnostic with its capability ID/parent/root cause, and writes `strict-ledger-findings.json`. The latest refreshed run exits 1 with **1,629 diagnostics across 236 distinct capability records** (226 route records plus 10 subsystem records). All 236 records have at least one unverified-status diagnostic. The run summary is in the JSON report; the full per-capability listing is in its `records` array.

| Diagnostic root cause | Count | Meaning |
| --- | ---: | --- |
| Record status not verified | 472 | Two checker diagnostics per record in this run; not two distinct capabilities |
| No source-backed invariant declaration | 163 | Ledger record has no invariant entry |
| No verified invariant | 163 | Invariant list is missing or has no verified invariant |
| Stateful initial scenario not verified | 113 | Separate initial-scenario requirement |
| Initial scenario missing/unverified | 113 | Scenario matrix entry |
| Repeat scenario missing/unverified | 122 | Scenario matrix entry |
| Changed scenario missing/unverified | 121 | Scenario matrix entry |
| Force scenario missing/unverified | 113 | Scenario matrix entry |
| Failure scenario missing/unverified | 117 | Scenario matrix entry |
| Missing `repeat_operation` invariant | 128 | Stateful/repeat contract not recorded as verified |
| Missing stateful true/false classification | 3 | Subsystem classification absent |
| Other | 1 | Unclassified validator diagnostic; inspect the machine report |

Stateful-scenario diagnostic totals overlap and should not be added to infer unique scenario gaps. There are **163** records with no invariant declarations and the same **163** with no verified invariant; the checker is reporting both requirements for those records. Separately, **128** records need a repeat-operation invariant. Exact records and root causes are listed in the machine report rather than duplicated in this document.

## Behavioral mismatches versus evidence gaps

The validator does not report implementation behavior. Therefore the count of *actual behavioral mismatches* cannot be derived from the diagnostic count; the report deliberately records this as `null` (unknown, not zero). Existing differential evidence identifies W5 as **11/11 PASS, zero divergences**, W6 as **9 scenarios, zero divergences**, the recorded default-mode sampled HTTP suite as **129/129 PASS**, a separate proxy-support run as **137 passed, 0 failed, 1 skipped** with nine email-device cases passing, and the latest default-mode SMTP test run as **144/144 PASS** with six SMTP setup/delivery/failure observations. These bounded probes do not prove the remaining untested capabilities match. Historical documented divergences in `TASK-20261030.md` are retained with their provenance and superseded/resolved status; do not aggregate them as current defects without a fresh reproducer.

The first W5 checkpoint rerun used a stale Go binary and reproduced two ACL mismatches; the rerun after rebuilding from the current tree passed all 11 cases. Both outcomes are retained in temporary reports under `/private/var/folders/jt/2prvbq7965ggn35rz2vwnqrw0000gn/T/opencode/w5-checkpoint-*`; only the fresh-built run is current evidence. This is a reproducibility warning, not a new source defect.

## Risk-group mapping

| Parent / subsystem | Diagnostic count | Risk-prioritized next focus |
| --- | ---: | --- |
| `http/catalog` | 393 | Catalog mutations, covers/downloads, scoped reads and deletion cascades |
| `http/userapi` | 375 | Ownership/ACL, progress, collections/playlists and persisted mutations |
| `http/adminops` | 171 | Settings, user administration, auth configuration and backup operations |
| `http/sharing` | 109 | Share access, expiry/revocation, media exposure and ACL |
| `http/podcasts` | 108 | Explicit-content gates, feed/episode persistence and download jobs |
| `http/playback` | 102 | Playback lifecycle, seek/progress persistence, restart/reconnect |
| `http/identity` | 64 | Local/OIDC authentication, state, token/session persistence |
| `http/notifications` | 33 | Recipient ACL and delivery effects |
| `http/backup` | 54 | Restore, malformed archive, DB/filesystem consistency and rollback |
| `http/providers` | 50 | Remote-provider errors, mapping and settings |
| `http/emailapi` | 32 | Send behavior, recipient/device/library/tag ACL and failures |
| `http/audiotools` | 48 | File mutations, invalid inputs, partial failure and persistence |
| `http/scanner` | 29 | Initial/repeat/changed/force/failure state and event parity |
| `http/server` | 20 | Server info/settings/client-facing behavior |
| `http/unassigned` and non-HTTP subsystem groups | 47 | See exact parent totals in `strict-ledger-findings.json` |

Counts are diagnostic occurrences, not affected behavior counts. Security/ACL, destructive persistence, and client playback take priority over reducing checker output. The next executable work is notification test/delivery and ACL; send-to-device attachment delivery and SMTP TLS/auth/error variants; broader download variants (Apple-mobile MIME, X-Accel, disk errors); OIDC cookie/expiry/error variants; and realistic browser playback with play/pause/seek/progress/reconnect/restart. Then expand route/event packets by parent and risk. Retention rolloff with controlled time remains an explicit W6 gap.
