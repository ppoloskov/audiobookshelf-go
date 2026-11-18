# Replacement inventory reconciliation

2026-11-13, mapping packet Q00. Source/test inspection only; product execution is
not claimed. Governing scope is `docs/agent-work/CONTRACT.md` and ARCHITECTURE.

The generated [mapping](replacement-requirement-map.md) is the requirement-to-test
view. The [queue](replacement-gap-queue.md) sequences its closure. This document
records discovery provenance and limits, not another progress ledger.

## Independent inventories

- All 226 `source_inventory.py` route sites have exactly one retained route ID:
  223 HTTP endpoints plus dynamic SPA, catch-all and regex response caching.
  This resolves the older coverage.md denominator of 223. r016 is response
  caching, not an authorization middleware; its cache isolation/freshness is a
  requirement even though implementation structure may differ.
- 165 syntax Socket.IO sites include repeated emitters and six generic dynamic
  forwarding sites. Literal callers resolve those wrappers, including Stream's
  clientEmit calls which the syntax extractor does not directly count. There
  are 77 name/direction records, including connection as a transport hook.
  Each literal source site is represented. Recipient hints are not runtime
  proof; each emitter/consumer still needs payload/audience/state assertions.
- All 25 model files map to schema and persisted-column requirements. Schema
  tests additionally cover the migrationsMeta table, which has no model file.
  Row/column semantics and filesystem formats are separate from boot success.
- Crosscutting records cover metadata.json, backups, logs, atomic settings,
  ranges/cache/MIME, configuration and binaries, runtime graph lifecycle,
  Node-derived observable ordering, scanner precedence/timelines and actual
  client workflows. Broad records are open obligations, not evidence that
  every branch is already specified or tested.
- The source and retained bundled client match pinned upstream edb1f7ef; the
  renderer refuses source/client drift and records their content digest. The
  rewritten Go base is 8990c2e61a1637a69effc4ca26e2df1748301889 with the existing
  accepted playback overlay and unrelated harness/status overlays preserved.

## Previously unresolved bundled-client calls

`contracts/client-usage.json` lists nine variable-based calls and one generic
unmatched path. Direct source inspection resolves them as follows. These are
request/consumer evidence, not successful test executions.

| Client source | Resolution | Requirement |
|---|---|---|
| cards/LazyBookCard.vue:744-750 | PATCH /api/me/progress/item[/episode] | route-r111 |
| readers/ComicReader.vue:110-115,233 | ebookUrl is /api/items/item/ebook[/file] | route-r069 |
| readers/EpubReader.vue:95-100,319-332 | custom fetch receives ebookUrl; ebook assets resolved by reader | route-r069; ebook engine remains approved exclusion |
| readers/MobiReader.vue:32-37,96 | same ebookUrl, blob fetch | route-r069 |
| tables/playlist/ItemTableRow.vue:193-197 | PATCH progress with optional episode | route-r111 |
| tables/playlist/ItemTableRow.vue:210-214 | DELETE playlist item with optional episode | route-r095 |
| pages/account.vue:210-214 | POST /logout, optionally allDevices=1; consumes redirect_url | route-r006 |
| pages/share/_slug.vue:31-35 | GET /public/share/slug with optional t seek parameter | route-r220 |
| players/PlayerHandler.js:179-197 | POST play with optional episode and device/MIME/transcode choices | route-r059, route-r060 |
| ui/QueryInput.vue:77 | generic /api/endpoint; source calls misspelled $gest; no concrete backend request established | client-device-acceptance; any concrete backend consumer discovered must be added, no route removed |

Client paths above are relative to `client/components/` except pages and players,
which are relative to `client/`. The QueryInput typo is recorded as a source
client discrepancy; repairing the unchanged client is outside this packet.

Native/third-party source pins and concrete request/consumer evidence remain in
`contracts/clients/{official-app,lissen,audiobooth,absorb,storii,yaabsa}.md`.
The mapping's seven client-flow records identify their cross-route obligations.
The route register covers all retained upstream routes, rather than treating
absence from a bundled-web scan as permission to remove a route. Absorb consumes
series_added and authors_num_books_updated; both remain registered. Client-only
task_updated/invalid_token listeners do not establish a pinned source emitter;
Q07/Q28 must reconcile actual recovery behavior rather than inventing emitters.
Source confirmation alone is not mobile/device acceptance.

## Corrections and gaps

Obsolete identity/userapi/sharing/store/bootstrap/catalog paths, production files
listed as tests, historical verified claims and missing invariant/state fields
were reconciled. Historical reports remain leads, with their limitations retained.
The accepted playback packet is preserved; reopening broad stale records does not
revoke its scoped architect acceptance or authorize another lifecycle rewrite.

Concrete source-contract corrections:

- metadata_embed_queue_update true means actually waiting, not immediate start;
  Go currently emits true/false for immediately started work. Q01 requires a
  failing source/client-backed regression and the smallest correction.
- notifications_updated is the whole settings document consumed by the config
  page, not just changed notifications. Its no-active-subscription admission edge
  needs characterization before deciding whether Go differs materially.
- backup_applied has no explicit source argument. Null versus no argument must
  be characterized at the Socket.IO consumer boundary.
- Six route leaves have no current named success proof: feed cover r008,
  unlink r077, all-progress r102, all-bookmarks r103, collection feed r189 and
  series feed r190. Many other mapped tests cover only denial, type skeleton or
  one state transition; their exceptions explicitly retain missing proof.

Old full-suite/core/ACL/OIDC/scan/boot reports contain representation mismatches,
fixture drift and possible semantic differences. Q02 must reproduce and classify
these; no historical red or green is silently treated as current behavior. The
current STATUS.md is preserved as the latest recorded core run, not rewritten
into a whole-contract verdict.

## Completeness and claim boundary

The source-site and named-reference checks are mechanical and reproducible. They
cannot prove exhaustive behavioral discovery, test strength, current runtime
outcomes or third-party/device compatibility. Each owner must refine sampled
invariants using source/client branches and append discoveries inside the fixed
contract. No denominator freeze or blanket unverified row can substitute for
that work. The final independent audit must reconcile source modules, consumers,
formats and lifecycle again before the final contract claim is permitted.

No production behavior, dependency, database schema, client build or deployment
was changed by Q00. No product tests were weakened or dropped. The mapping/tool
checks run in this packet are recorded in its canonical task ledger.
