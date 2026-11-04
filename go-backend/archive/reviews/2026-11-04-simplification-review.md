Review of the updated Audiobookshelf-Go
Your revised assessment of sqlc is right. It is a correctness and maintainability tool, not the main code-reduction lever.

The updated repository also reflects the architectural direction we discussed: preserve the existing SQLite database and client protocols, but allow the Go implementation between them to change freely.

The most important improvement is that this is now explicit across ARCHITECTURE.md, AGENTS.md, and ROADMAP.md.
My initial assessment
Area	Assessment
Architecture principles	Much improved; clear fixed contracts
Contract testing	Major advance; broader than the old differential suite
Library adoption	Sensible and already delivering targeted simplifications
sqlc	Useful, but its expected impact was overstated
Simplification strategy	Still too focused on migration tasks and line-count targets
Remaining risks	Client coverage, package boundaries, and verification before deleting compatibility infrastructure

The key question has changed from “How much SQL boilerplate can we generate?” to “Which responsibilities and transformations can disappear altogether?”
1. The new measurements change the roadmap priorities
The reported savings for L5 and L6 are:
L5 · Users and sessions
-58
Handwritten Go lines

L6 · Progress and playback sessions
-4
Handwritten Go lines


Those figures fit the code I inspected. For instance, store/playbacksession.go still contains PlaybackSessionFromRow, date conversion, nullable-value handling and LegacyJSON after adopting sqlc.

That isn't evidence of a failed migration. sqlc has removed manually maintained query execution and scanning for supported queries, but it cannot remove the distinction between database records and client representations.

The current ratchet records:
Metric	Initial baseline	Current recorded value
Handwritten non-test Go lines	44,326	44,586
Cyclomatic complexity total	10,961	10,851
Functions with complexity above 15	186	178
map[string]any uses	1,003	993
Handwritten Handle routers	13	9
Setter injectors	18	18

The net line count has increased by 260 during the foundation, testing-support and library work, while complexity has fallen. This isn't inherently bad, but it demonstrates why line count alone is a poor short-term success criterion.

I would finish L8 for the specific benefits of typed, checked queries, not to pursue a line-reduction target. I would also split L8 into smaller domain-owned tasks. Its current completion criterion—fewer than 100 raw SQL call sites outside the database layer—could encourage mechanical conversions that yield little architectural benefit.
2. The 30% reduction target needs a different plan
The roadmap targets approximately 31,000 handwritten lines by the end of Phase S, down from 44,326.

That requires removing about 13,300 lines.

S1–S15 largely describe moving routes, reorganising packages, introducing constructors and converting maps to structs. These can improve readability, safety and maintainability, but they do not inherently remove large amounts of code.

Some could temporarily increase it.

The most significant opportunities are instead:
- Deleting entire unused behaviours and implementations.
- Unifying the item-loading paths used by catalog, shelves, playlists and playback.
- Eliminating repeated transformations between dynamic maps, database records and API responses.
- Removing redundant lifecycle and compatibility infrastructure.
- Combining overlapping functionality across existing feature packages.
I would retain 30% as an aspirational outcome, not a hard gate that encourages agents to optimise source-code length at the expense of design quality.
3. The contract-test work is valuable, but coverage is being overstated
The new test infrastructure is one of the strongest improvements in the repository.

You now have a TS-created installation fixture, Go-native journeys, response-shape checks, Socket.IO event checks, and persistence tests in both directions. That's a much better foundation for refactoring than the old byte-differential suite.

However, the roadmap says all 201 of 201 client-used routes are covered. That needs qualification.

I inspected contracts/client-usage.json. It still records:
- 201 resolved routes.
- 1 unmatched route pattern, /api/:param.
- 9 unresolved dynamic Axios call sites.
- Mobile usage partly inferred from production logs and the replacement trial, because mobile source isn't in the workspace.
Also, contracttest/shape/shape.go checks structural conformance rather than value semantics. It permits extra fields, and some inferred structures are deliberately unconstrained.

That's appropriate for flexible client compatibility, but a response with the right fields and types can still contain incorrect progress values, ordering, permissions or URLs.

Recommendation: Separate route-shape coverage from behavioural coverage. Retain targeted assertions for actual values and state transitions on critical workflows.

I would prioritise O4—real iOS and Android acceptance—before deleting the remaining diagnostic tools.
4. The highest-risk architectural change is actually Socket.IO
A significant change has already landed: C25 replaced the external Socket.IO implementation with an in-repository Engine.IO/Socket.IO implementation.

See internal/realtime/eio.go and sio.go.

I understand why: the previous dependency had a connection/authentication race. Your implementation fixes the registration ordering.

But it also introduces a new maintenance responsibility:
Behaviour	Current implementation
Engine.IO v4	Supported
WebSocket transport	Supported
Long polling	Unsupported
Default namespace	Supported
Binary Socket.IO packets	Rejected
JSON events and acknowledgements	Supported

The source states that the web and mobile clients use WebSocket-only transport. That may be correct, but real mobile testing is still an unchecked owner task.

There is also a concrete hardening question: the server advertises a 1 MB maximum payload, but I don't see an explicit inbound message-size check in readLoop. Library defaults and oversized-message handling should be verified.

Recommendation: Keep the new implementation for now, but add protocol interoperability and resource-exhaustion tests, not just the custom test client's happy path.

This deserves more attention than saving another 100 lines through sqlc.
5. V2 has a dependency problem
The roadmap schedules deletion of the Node byte-parity harness after L8.

That is reasonable conceptually, but the implementation currently has dependencies on that harness:
- tools/contractfixture/build.cjs imports tools/parity/replacement-trial.cjs and tools/parity/lib/args.cjs.
- tools/clientusage/main.go reads tools/parity/replacement-trial.cjs and the parity route inventory.
- The existing generated fixture and client inventory need reproducible regeneration paths.
Deleting most of tools/parity without first extracting those dependencies would leave you with passing committed tests but broken tooling for regenerating their inputs.

I would change V2's completion condition: preserve the rollback check, move the fixture seed and route inventory into independent tooling, then prove both generators still work after the old harness is removed.

Also note that the roadmap says V2 will eliminate most of an approximately 11,000-line Node harness. That's valuable repository cleanup, but those JavaScript lines aren't included in the handwritten-Go metric. They cannot contribute to the 30% Go reduction target.
6. Documentation inconsistencies to correct
The core architecture documents are aligned, but some operational documentation has fallen behind.
File	Problem
README.md	Still describes parity as blocking until V1, although V1 is complete
handover.md	Says Socket.IO cover search is missing, although C5 is complete
handover.md	Still refers to the old differential-running lifecycle
ROADMAP.md	Priority section lists L6 and L7 as upcoming, although both are checked
ARCHITECTURE.md	Describes sqlc as replacing ~590 handwritten call sites without distinguishing typing benefits from actual code reduction
ROADMAP.md	The 30% reduction goal lacks enough identified deletion work to substantiate it

I would make the roadmap a genuinely current execution document: completed work belongs in a concise history, current priorities should contain only outstanding tasks, and measured outcomes should replace speculative savings.
7. The biggest remaining code opportunity is the catalog data flow
After looking at the implementations, this is where I'd redirect the main simplification effort.

internal/catalog/handler.go remains roughly 3,200 lines. scanItemRow constructs a dynamic map, while book_wire.go contains typed book models and personalized.go constructs shelves by loading individual items.

The same conceptual entity is assembled in multiple places for different consumers.
SQLite records

Loaded through sqlc or direct SQL

One typed item representation

Book · Podcast · Library item · Related entities

Item detail


Shelf summary


Playback




The goal is not necessarily one universal DTO. Different endpoints legitimately need different response shapes.

The goal is one authoritative place to obtain the underlying item data, and small, explicit projections for each client-facing shape.

That could eliminate more code than generating SQL accessors, while also addressing the N+1 query problem identified in P2 and P3.

I'd execute P2/P3 together with S13, before undertaking most of the package moves in S10–S15.
Revised execution priorities
My suggested sequence is:
Priority	Work	Main value
1	Finish L8 selectively	Typed, checked SQL
2	Complete mobile acceptance and harden Socket.IO	Protect client compatibility
3	Complete R1/R2 and approve removable functionality	Actual code deletion
4	Build one typed, batched item-loading path (S13 + P2/P3)	Largest plausible structural reduction
5	Remove Express framing and obsolete runtime paths	Low-risk simplification
6	Consolidate permissions, constructors and routing	Lower coupling
7	Carve domains out of remaining packages	Better ownership
8	Retire parity tooling after extracting its reusable inputs	Reduced maintenance

I would also change the ratchet policy slightly. Continue measuring handwritten lines, complexity, dynamic maps and duplicate implementations, but use them as review signals rather than unconditional constraints for every ordinary refactor. A temporary increase in code can be justified if it eliminates coupling, reduces runtime risk or enables a larger subsequent deletion.
Overall conclusion
The updated architecture is substantially better than the previous version, and the testing foundation is now credible enough to support major internal changes.

Your correction about sqlc is important because it exposes a broader planning issue: several roadmap tasks are valuable refactors but are being implicitly counted as code-reduction opportunities.

I would not abandon sqlc. I would abandon the expectation that converting data-access syntax or moving code into domain packages will deliver a 30% reduction.

The strongest next move is to identify whole behaviours to eliminate and repeated item/data transformations to unify. That is where the remaining simplification potential lies.

This was a read-only review of the current repository, source files, roadmap and documentation. I did not run the Go test suite or independently reproduce the L5/L6 commit-level line deltas.