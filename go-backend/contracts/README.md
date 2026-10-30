# Legacy source compatibility inventory

`source-inventory.json` is a reproducible, machine-readable index of syntax found in the upstream Node backend. Regenerate it from the repository root with:

```sh
python3 go-backend/tools/source_inventory.py --output go-backend/contracts/source-inventory.json
```

Run the extractor's fixture tests with:

```sh
python3 -m unittest -v go-backend/tools/test_source_inventory.py
```

The checked inventory was generated from git revision `edb1f7efc92ecacdef51aafb4d9d5054ce10604f` (Audiobookshelf v2.37.2). It records source SHA-256 values and source locations to make each extracted item auditable.

## Inventory scope

This is source extraction, not a complete API or behavior specification. It records route registration syntax, literal or unresolved route expressions, a syntax-only argument count hint, potential Socket.IO event registrations/emissions, and filenames under `server/models`. The route prefix annotations capture the known `/api`, `/public`, and `/hls` mounts where applicable, but the configurable `global.RouterBasePath` is unresolved. Routes in other contexts may have unknown mount points. Middleware chains, authentication and authorization, conditionals, route ordering semantics, request and response shapes, handler behavior, database schema, metadata serialization, and Socket.IO delivery scope are not interpreted.

The extractor intentionally keeps dynamic expressions and regex paths as source text instead of guessing their runtime values. The Socket.IO scanner uses receiver-name heuristics and can include other event-emitter calls; conversely, it cannot prove that every event reachable at runtime was found. Review the referenced source alongside any compatibility decisions.

## Snapshot counts

| Kind | Count |
| --- | ---: |
| Route registrations | 226 |
| Socket event registrations and emissions | 165 |
| Model source files | 25 |
| Distinct source files hashed | 32 |

Among the 226 route candidates, 224 have string literals, one has a regular-expression argument, and one is a dynamic expression. Among 165 socket event candidates, 159 use string literals and six are dynamic expressions. The socket count includes 10 direct incoming registrations, 23 direct emissions, and 132 emissions through `SocketAuthority` wrappers. Wrapper emissions record the event argument position and a scope classification derived from the wrapper signature (`emitter`, `clientEmitter`, `adminEmitter`, `libraryItemEmitter`, or `libraryItemsEmitter`); actual delivery is still unresolved.

The current extractor scans JavaScript `.js` files. It does not scan TypeScript files, and its regular-expression based syntax matching can count route or socket registrations appearing in comments or strings. These counts describe extractor output for the recorded source revision, not guaranteed runtime endpoint or event counts.
