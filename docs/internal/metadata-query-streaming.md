# Metadata query streaming and pagination

Status: initial implementation. Existing unary APIs remain unchanged.

## Scope

Add a multiplexed frontend WebSocket for Series, LabelNames, and LabelValues.
The frontend uses a new server-streaming QueryBackendService.InvokeStream RPC.
Only QUERY_SERIES_LABELS, QUERY_LABEL_NAMES, and QUERY_LABEL_VALUES are allowed
on this RPC. Reject unsupported or malformed queries before executing a plan.

Two independent boundaries apply:

- Internal RPC chunks: bounded by serialized, uncompressed protobuf bytes.
- Public pages: bounded primarily by element count (default and maximum 10,000),
  with a serialized JSON byte safety cap.

Neither boundary is a limit on the total query result. Total results and resource
usage have separate, explicit limits; exceeding them is an error, not truncation.

## WebSocket protocol

Use the authenticated `/querier.v1.QuerierService/Session` endpoint on v2 and
mixed v1/v2 query-frontends. This endpoint always queries the v2 backend, independently
of the legacy unary read-path router. Preserve tenant authorization, query validation,
timeouts, and same-origin browser checks. Non-browser clients may omit Origin.

Negotiate representation before upgrade using an `accept` URL parameter. Initially
only `application/json` is supported; reject unsupported representations before
upgrading. Do not enable WebSocket compression. Use JSON text messages only.

Wire messages are defined in `api/querysession/v1/querysession.proto`: `SessionRequest` and
`SessionFrame` use typed `oneof` payloads. Both frontend and profilecli use the
same generated messages and protobuf JSON codec with proto field names. This
package trials Go's opaque API: adapters use builders, getters, and `WhichPayload`,
not exported message fields or oneof wrappers. It has its own public QueryType
enum and does not import the backend query schema. No vtprotobuf code is generated
for it. Int64
query times are encoded as JSON strings; enum values use their protobuf names.
Unknown fields, duplicate fields, and multiple oneof payloads are rejected.
The small Go Request/Frame value types are normalized execution representations,
not a second JSON schema; their JSON methods delegate to the generated messages.

Each request has a connection-local request ID. IDs must be unique among in-flight
requests. Responses, errors, and completion messages carry that ID. A single writer
serializes outbound frames while independent queries may execute concurrently.

Request operations:

- query: query type, time range, selector, operation-specific arguments, page size.
- next: opaque next_token and optional page size, using a new request ID.
- cancel: target request ID; idempotent cancellation of active execution.
- reset_string_table: requests a reset at the next server writer boundary.

Response operations:

- page: operation type, attribute references using the connection's shared string
  table, elements, optional next_token.
- complete: terminal successful completion of the page request. Its
  `query_complete: true` flag signals successful completion of the entire query;
  absence of that flag means remaining results are still provisional.
- error: terminal request-scoped error with a stable code and safe message.
- table_reset: authoritative reset barrier carrying the new table epoch. An empty
  request ID identifies an unsolicited server-initiated reset.

A next_token indicates that the completed page request has more pages available.
No token means the result is exhausted. Empty results produce an empty page and
completion. Query errors never masquerade as successful partial results.

Malformed framing is a connection-level error; ordinary query errors do not abort
unrelated requests. Bound inbound frames, active requests, output queues, result
storage, and connection idle time. A slow reader must not cause unbounded buffering.
On disconnect cancel active work, including paused backend streams; cached pages
may be replayed until expiry, but an unfinished session cannot continue and requires
a query restart. Reconnect does not silently restart or duplicate backend execution.

## Connection-wide string interning

All multiplexed requests on one WebSocket share a connection-scoped string table.
This is separate from the internal protobuf AttributeTable, which remains local
to each backend chunk. Intern both label names and values across operations and
pages, rather than repeating strings per request.

Initially requests carry ordinary JSON strings; the shared table encodes outbound
results only. A future inbound-reference format must explicitly define separate
directional dictionaries and negotiation rather than implicitly sharing ID space.

The server assigns monotonically increasing, connection-local integer IDs within
each table epoch. Epoch starts at zero. A single writer owns ID assignment,
reset barriers, and frame serialization. Each page frame carries
an ordered `strings` delta with an explicit starting ID, followed by its payload.
Clients apply the delta before decoding references in that frame. An empty delta
means all referenced strings were announced previously. Never reference a string
from a frame that has not yet been written.

Series elements contain label key/value string-ID pairs; LabelNames and LabelValues
elements contain string IDs. The wire representation is a shared string dictionary,
not the internal parallel key/value AttributeTable representation.

IDs are append-only within an epoch. Resets are possible from either end:

- A client sends `reset_string_table` with a fresh request ID, but does not clear
  its table yet. The server emits `table_reset` with that ID, then completion.
- The server initiates a reset when a page would exceed the connection dictionary
  budget and that page fits into a fresh dictionary. This reset has no request ID
  and requires no client acknowledgement.

Each reset increments the epoch exactly once and restarts IDs at zero. A client
clears its table only upon receiving the ordered reset barrier. Every page carries
its table epoch; clients reject stale/future epochs and nonconsecutive resets.
The server serializes resets with page encoding/writes, so no old-epoch reference
can appear after a reset, including with multiplexed queries and simultaneous
client/server reset initiation. Epoch exhaustion requires a new connection.

Resets do not cancel queries or change cursor positions, deduplication, or cached
logical pages. Cached pages are re-encoded against the current epoch on replay.
Dictionary accounting is released on reset. A page that cannot fit even in a fresh
dictionary still fails with an explicit resource-limit error. A new connection
starts with an empty table at epoch zero.

Include table deltas in JSON frame byte accounting. Build frame deltas transactionally
under the writer's ownership so splitting or rejecting a frame cannot leave gaps or
unannounced references. Dictionary growth is charged to the connection and tenant,
not to whichever query first happened to introduce the string alone.

## Pagination sessions

Consume backend results incrementally, deduplicating until the first page is
filled or the stream ends. Sort that page and return it without waiting for the
complete result. Continue the same process for subsequent pages, retaining an
exact seen set across pages so an element is never returned twice.

Ordering is page-local, not global: a later page may contain an element that sorts
before an element on an earlier page. Sort label names/values lexicographically.
Sort Series by canonical label-name/value sequences, with labels inside each set
sorted by name and value. Use exact equality, not hash equality alone, for
deduplication. A page is filled when its element or JSON byte bound is reached.

Count one label set as a Series element and one string as a LabelNames/LabelValues
element. Page size defaults to 10,000 and cannot exceed 10,000. The byte cap may
produce a smaller page. A single element exceeding the byte cap fails explicitly;
never return an empty page with a non-advancing cursor.

A cursor references TTL-bound server-side query session state and a page position.
The session owns the active backend stream, pending elements, and cross-page seen
set. Tokens are opaque, unguessable/authenticated, and bound to the authenticated
tenant identity and operation. Serialize page generation per session. Cache each
generated page for replay; concurrent or repeated requests for the same token must
not advance the stream twice. Replaying a token returns the same page for the same
page size; reject a different page size once that token's page has been generated.
Never trust client-provided tenant identity or offsets.

If a full page is ready before stream termination is known, return a next_token;
following it may yield an empty final page. A backend error after earlier pages
were delivered terminates the session with an error: earlier pages are provisional
and do not constitute a successful complete query.

Tokens do not survive frontend replacement or load balancing to a different
frontend. Cached pages can be replayed after reconnect to the owning frontend, but
disconnect cancels unfinished execution: generating further pages requires a
query restart. Expired or unavailable state requires a restart.
Release exhausted sessions after a bounded replay grace period, or at TTL expiry.
Cancel unfinished streams when sessions expire or are evicted. Cancellation and
explicit resource cleanup must not leak session state or goroutines.

Pause consumption between page requests using bounded buffering/backpressure.
Enforce per-tenant and per-process accounting for streams, pending elements, cached
pages, and the cross-page seen set, which can grow with the entire result. Byte
accounting must include table/string storage and not only element count.

## Internal InvokeStream

Add a server-streaming RPC with a dedicated response type, preserving Invoke and
its wire format. Each result chunk identifies its query (request query index),
operation type, and contains a local AttributeTable plus operation-specific refs.
Every chunk can be decoded without previous chunks.

Reuse `pkg/model/attributetable`: its table is a dictionary of distinct key/value
pairs, represented by parallel keys and values arrays, not two independently
interned string dictionaries. References index corresponding entries in both.

- Series: one list of attribute refs per label set.
- LabelNames: refs to entries with the name in key and an empty value.
- LabelValues: refs to entries with the queried label name in key and value in value.

Validate table lengths and all references when decoding; existing permissive
resolvers must not silently discard malformed references on this new protocol.

Chunk size accounting includes the entire protobuf response envelope and table.
Use a fixed 4 MiB bound initially. Account for new
dictionary entries when adding an element. Emit the current chunk before it would
exceed the bound; reject an individually oversized element. Avoid reserializing
the entire growing chunk for every element: maintain incremental size accounting
and verify the completed response with protobuf size calculation.

All remote backend aggregation hops must use InvokeStream. Leaves can initially
materialize their local result, but must encode bounded chunks before transport.
Merge nodes forward independently decodable child chunks with bounded concurrent
fan-out, without materializing a globally merged result. Cross-child deduplication
is performed in the frontend session. A streaming wrapper around unary remote
Invoke is insufficient:
it preserves the existing oversized child-RPC failure.

Carry diagnostics/statistics once per invocation, not once per data chunk, and
include their envelopes in size bounds. Successful gRPC EOF marks completion;
non-OK termination invalidates the invocation. Cancel siblings on failure. Do not
transparently retry a partially delivered stream into an existing aggregator:
either disable that retry or discard that child's partial contributions first.

The frontend incrementally repackages internal chunks into deduplicated,
page-locally sorted pages using the connection-wide string table. Internal chunk boundaries
do not determine public page boundaries. Intermediate backend merging must not
wait for global result materialization: forward bounded chunks, leaving
cross-chunk/global deduplication to the frontend session.
Measure the entire final JSON frame for the byte safety cap, including escaping,
request ID, string-table delta, envelope, and token. Compression is not required
for correctness.

## Implementation touchpoints

- `api/query/v1/query.proto`: InvokeStream and compact metadata chunk messages.
- `api/querysession/v1/querysession.proto`: WebSocket request/response oneofs, tables, and resets.
- `api/buf.gen.querysession.yaml`: opaque Go-only generation, no vtprotobuf.
  `make generate` excludes querysession from the legacy template and generates it
  separately using this template.
- `pkg/frontend/querysession/protocol.go`: normalized-value adapters using protobuf JSON.
- `pkg/querybackend/backend.go`: streaming read/merge execution and diagnostics.
- `pkg/querybackend/client`: streaming calls, cancellation, and retry behavior.
- `pkg/model/attributetable`: existing dictionary/remapping implementation.
- `pkg/frontend/readpath/queryfrontend`: planning, result collection, and pagination.
- Frontend HTTP routing/authentication: WebSocket endpoint and admission limits.
- Regenerate protobuf, gRPC, and Connect outputs using repository generation tools.

## Required tests

- Only the three supported operations are accepted; unary behavior unchanged.
- Multiple backend hops, large results, table remapping, exact deduplication.
- Strict protobuf size bounds, oversized elements, empty results, UTF-8 strings.
- JSON escaping/size bounds and element boundaries; no missing or repeated elements.
- Page-local ordering, cross-page deduplication, and first-page delivery before EOF.
- Deterministic cursor replay, concurrent next requests, expiry, wrong-tenant tokens.
- Backpressure between pages, late backend failures, and disconnect cleanup.
- Multiplexed out-of-order responses, duplicate active IDs, cancellation/disconnect.
- Mid-stream failure, retry handling, partial-result invalidation, diagnostics totals.
- Admission limits, slow readers, snapshot eviction, cleanup, and race tests.
- JSON-only negotiation, rejected binary messages, origin checks, compression disabled.
- Cross-request string reuse, serialized dictionary deltas, reference validity,
  dictionary budgets, transactional frame splitting, replay on a fresh connection.
- Protobuf JSON round-trips, oneof/unknown-field rejection, 64-bit time encoding.
- Client/server resets, reset/page ordering under multiplexing, stale epoch rejection,
  replay across resets, and dictionary accounting release.

## Initial defaults and limitations

Flags use `query-frontend.metadata-streaming.*`; YAML uses `frontend.metadata_streaming`.

- Session TTL and connection read-idle timeout: 5 minutes.
- JSON frame cap: 4 MiB; inbound request cap: 64 KiB; write timeout: 10 seconds.
- Active page requests per connection: 8.
- Sessions per tenant/process: 16 / 256; connections per process: 256.
- Accounted memory per tenant/process: 64 MiB / 256 MiB.
- Connection dictionary: 100,000 entries and 16 MiB accounted memory.
- Page size: default/maximum 10,000, fixed for a query.
- Completed page caches remain until the original session TTL expires.

Memory accounting is conservative logical accounting, not a bound on Go heap/RSS:
transport buffers and protobuf allocation overhead also require operational sizing.
Each session reserves 4 MiB plus 128 KiB for an incoming chunk and input/page
overhead.
Federated queries are charged to each participating tenant and once to the process.
Frontend shutdown explicitly closes hijacked sockets and expires sessions.

The initial endpoint applies legacy label-name filtering. UTF-8 capability
negotiation and legacy metadata-only profile-type compatibility optimizations are
not implemented here. Leaf block readers still materialize their local results;
remote aggregation and frontend pagination are incremental.

## profilecli REPL

Run `profilecli repl --url http://localhost:4040 --tenant-id <tenant>` (also available
as `profilecli query repl`). Existing `--token`, `--username`, `--password`, and
`PROFILECLI_*` connection environment variables apply to the WebSocket handshake.
URL path prefixes, such as Grafana datasource proxy URLs, are preserved.

```text
from now-2h
selector {service_name="abcd"}
series
label_names
label_values job
next q1
cancel q1
```

`to now`, `page_size 10000`, `reset_string_table`, `show`, `help`, and `quit` are also supported.
`series job service_name` projects specific labels. Each query gets an ID; `next`
and `cancel` without an ID target the latest query. New queries may run while
others are pending. Page size is fixed for existing sessions; changing the default
only affects new queries. Relative time defaults are evaluated when each query
is sent. EOF waits for outstanding page requests; `quit` immediately closes the
socket and cancels outstanding work.

Transport disconnects (including the five-minute server idle timeout) reconnect
automatically, starting after 250 ms and retrying failures with exponential backoff
capped at 10 seconds. Local commands and `quit` remain responsive during retries.
The selector, time defaults, page size, and request ID sequence are preserved.
Each new connection starts with an empty string table at epoch zero. Old query IDs
and cursors are marked disconnected; `next` and `cancel` reject them locally.
Unfinished or possibly delivered requests are never replayed: the user must run
those queries again, and previously printed partial results remain provisional.
Late events from old connection readers are ignored. Protocol violations are fatal,
not retried. Noninteractive EOF reports an error if a disconnect lost outstanding
requests, rather than treating partial output as successful completion.

The CLI accepts at most 4 MiB per JSON frame and 100,000 interned strings, with a
32 MiB string-table accounting cap. It also caps rendered page output at 16 MiB
to guard against expansion of repeated references. Server errors and result
strings are quoted to avoid interpreting terminal escape sequences.

## JSON example

Connect to `/querier.v1.QuerierService/Session?accept=application/json` and send:

```json
{"id":"q1","query":{"query_type":"QUERY_SERIES_LABELS","start":"1000","end":"2000","matchers":["{service_name=\"example\"}"],"page_size":10000}}
```

A page introduces strings and then references zero-based IDs shared by the socket:

```json
{"id":"q1","page":{"query_type":"QUERY_SERIES_LABELS","table_epoch":0,"strings":{"start":0,"values":["service_name","example"]},"series":[{"labels":[{"key":0,"value":1}]}],"next_token":"opaque-token"}}
{"id":"q1","complete":{}}
```

Continue with a new request ID:

```json
{"id":"p2","next":{"next_token":"opaque-token"}}
```

LabelNames and LabelValues pages use `refs: [0,1,...]` instead of `series`.
Cancel a query using its original request ID, including while paused between pages:

```json
{"id":"cancel1","cancel":{"target_id":"q1"}}
```

A final page has no next_token and its completion carries `query_complete: true`.
A full page may be followed by an empty final page because EOF is not awaited
before returning the full page. Protobuf JSON may omit zero-valued scalar fields.

Client-initiated reset:

```json
{"id":"reset1","reset_string_table":{}}
```

Server responses, ordered with every other outbound page:

```json
{"id":"reset1","table_reset":{"epoch":1}}
{"id":"reset1","complete":{}}
```

An automatic server reset instead has no ID:

```json
{"table_reset":{"epoch":2}}
```

The next page then uses `table_epoch: 2` and IDs starting at zero.
