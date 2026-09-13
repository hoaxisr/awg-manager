# Adding an MCP tool to awg-manager

An MCP tool is a thin, well-described door from an AI agent into the daemon. The
daemon side already exists; the work is mostly about what the agent is told and
what it is allowed to believe. Most bugs found in review were not in the router
logic but in the tool lying to the model: a truncated list read as complete, a
zero read as "0 ms", a fake that agreed with the code instead of the real
service.

## The six places a tool touches

Every tool needs all six, in this order. Skipping one produces a compile error
at best and a silent hole at worst.

| # | File | What goes there |
|---|------|-----------------|
| 1 | `internal/mcp/types.go` | Plain data types crossing the boundary. Add `jsonschema:"…"` tags that say what a field MEANS to a model, not just its type. |
| 2 | `internal/mcp/deps.go` | The `Deps` method. The comment states the contract the tool relies on (ordering, what an error means, what nil means). |
| 3 | `internal/mcp/tools_<area>.go` | The tool: validation, annotations, description, output shaping. Register it in `server.go` if it starts a new `register*` group. |
| 4 | `internal/mcp/mcptest/fake.go` | The in-memory fake. It must mirror the REAL service's semantics — see "The fake must not agree with you" below. |
| 5 | `internal/mcp/localdeps/localdeps.go` | The production adapter over the daemon's services. Linux-only; see "Testing". |
| 6 | `internal/mcp/scope.go` and `server_test.go` | Add read-only tools to `readOnlyTools`; add every tool to the catalogue list in `TestServer_ListsToolsWithAnnotations`. |

Wire new daemon services into `localdeps.Config` from
`internal/server/server_routes.go`. Assign the CONCRETE type there, never an
interface field of the server: an interface holding a nil pointer passes a nil
check and panics on first use.

## Write the tests first, in both packages

Tool-level tests live in `internal/mcp/tools_<area>_test.go` and drive the
real MCP transport through `newTestSession` / `callTool`. Adapter tests live in
`internal/mcp/localdeps/localdeps_test.go` and use partial fakes of the daemon
services (`fakeDNSRoutes`, `fakeRouter`, …). Write both before the
implementation and watch them fail for the right reason: "unknown tool" or a
missing method, not a typo.

After the test passes, break the implementation once — reverse an order, drop
a check — and confirm the test fails. Two review findings came from tests that
passed only because the fake and the code shared the same wrong assumption.
This check is cheap and it is the only proof the test can catch anything.

## The fake must not agree with you

`mcptest.Fake` exists so tool tests run on macOS and in `cmd/mcp-dev`. It is
worth exactly as much as its fidelity to the real service. Before writing a
fake method, read the real one and copy its semantics, and cite the source in
a comment so the next reader can re-check:

- **Ordering.** `logbuf.Buffer.GetAll` returns newest first. A fake that
  returned oldest first let an adapter reverse an already-correct list.
- **Id spaces.** The NDMS server list excludes servers awg-manager manages;
  the managed service knows only those. A fake serving both from one slice hid
  that no peer tool could complete end to end.
- **Search semantics.** `connections.ListParams.Search` is a substring match
  over `src dst clientName`, not an exact source filter.
- **Zero values.** `DelayChecker.CheckOne` answers 0 both for a timeout and for
  "already probing". `dnsroute.Update` treats a zero field as "not sent".
- **Which lookup finds what.** `dnsroute.Get` scans only the JSON store;
  `List` also merges HydraRoute lists with `hr:` ids.

## Shape the output for a model, not a UI

- **Say what a value means when it is ambiguous.** A `0` latency needs a
  separate `reachable` flag; a busy probe needs `busy`. An omitted action is
  `route` in sing-box, so report `route`, not `""`.
- **Never let a page look like the whole.** Cap lists (`MaxDomainsInOutput`,
  `MaxConnectionsInOutput`) and always return the real total and a
  `truncated`/`hasDraft` flag beside the page. An agent answers "not in the
  list" from a truncated page unless told otherwise.
- **Return the record after a write, read back.** Not the input echoed. If the
  read-back fails, say the state is unknown (`stateKnown: false`) rather than
  returning a zero struct that reads as "stopped and disabled".
- **Report what you could not evaluate.** `explain_route` lists lists it could
  not judge (`geosite:` tags, unreadable lists) instead of letting an empty
  match mean "no rule covers this". Silence reads as all-clear.
- **Staged writes must say so in text.** A sing-box rule edit lands in a draft.
  Structured `staged: true` is not enough — attach a `TextContent` sentence,
  because a skimming model reads any successful result as "done".
- **Redact what a reader must not carry away.** Subscription URLs embed
  tokens in the query or userinfo; `redactURL` keeps host and path only. A
  read-only key is meant for an agent you do not fully trust.
- **Warn about losses the caller did not ask for.** Re-pointing a multi-target
  list to one tunnel drops the others; return `warnings` naming what was lost.

## Validate before Deps, and refuse with the reason

Every free-text input an agent can set is attacker-controlled: the agent
may be following a prompt injection. Ask where each string ends up, not
only whether it is well-formed.

- A value that is rendered into a file another program executes is a
  config injection. A peer's `dns` is written verbatim as `DNS = …` into
  the client `.conf`; `"1.1.1.1\nPostUp = curl … | sh"` runs on the user's
  machine when they import it. Such fields take a closed grammar (a list of
  IPs, an enum), validated in the tool AND in the service so the REST path
  is closed too.
- Names and descriptions travel to NDMS, the settings file and the journal.
  Cap them (`MaxPeerDescriptionRunes`) and reject control characters.
- Anything handed to the router's resolver or an outbound probe is bounded
  first (`maxTargetLen`), and the probe target itself is fixed, never
  caller-chosen.

- Tunnel ids go through `requireTunnelID` before anything else: the store joins
  the id into a file path, so `../settings` must never reach it.
- IPs go through `net.ParseIP` and are forwarded in canonical form only.
  `" 192.168.1.10"` and `::ffff:192.168.1.10` must find the same route.
- Unknown ids are errors, not empty results. A latency probe on a mistyped tag
  would otherwise report a healthy proxy as down.
- When an id exists but the tool cannot serve it (an unmanaged server, a rule
  generated by the daemon), say WHY. "Not found" sends the agent hunting for a
  typo in an id it was just given.

## Annotations and scope decide what the host asks the user

Use the three helpers in `server.go`:

- `readOnly` — changes nothing. Also add the name to `readOnlyTools` in
  `scope.go`, or a read-only key gets a puzzling refusal. The drift test
  `TestScope_ReadOnlySetMatchesTheAnnotations` fails if the two disagree.
- `safeWrite(title, idempotent)` — reversible. Prefer explicit values over
  toggles (`enabled: true/false`, never "flip") so a retry after a timeout
  cannot undo itself.
- `destructiveWrite` — cannot be undone through MCP: list removal, discarding
  a shared draft. Return the destroyed record so the agent can show the user
  what is gone. Hosts prompt the user on this hint; do not spend it on
  reversible actions or users learn to click through.

A delete that lands in the sing-box staging draft is a borderline case: the
draft can be discarded until it is applied, but after apply nothing in MCP
recreates the rule. Treat it as `destructiveWrite`. The hint is about what
the agent can undo on its own, and "ask the user to discard a draft that may
hold their other edits" is not that. Two agents given this case chose
differently; settle it this way.

A tool that changes nothing but returns private keys (`export_tunnel_config`,
`get_server_peer_config`) is annotated read-only AND listed in
`credentialTools`, so a read-only key is still refused. The UI promises that
key "cannot change anything"; walking away with VPN credentials is not that.

## Secrets stay behind the boundary

Peer private keys and preshared keys, proxy usernames, MCP key hashes: none of
these cross `Deps`. Map fields explicitly rather than converting whole structs.
Add a test that greps the tool's JSON output for the secret's value. The one
tool that must return a private key says so in its description in capitals and
tells the model to hand it to the user rather than repeat it.

## Persisted formats

If a tool needs a new persisted field (a key scope, a flag in a store file),
bump that file's version constant. An older build reads the file as its own,
rewrites it on the next save without the field, and the field is gone after
the next upgrade. For `mcp_keys.json` that meant a read-only key silently
becoming full-access.

Write the new version only when the file actually carries the new field
(`fileVersionFor` in `mcp_keys.go`). Stamping every save with the newest
number makes an older build refuse the file even when nothing in it needs
protecting, which turns a routine downgrade into an MCP outage.

## Regenerate, never hand-edit, the spec chain

A field added to an API DTO (`internal/api/*.go`) flows through two
generated files, and CI checks both for drift:

```
Go annotations  →  internal/openapi/swagger.yaml  →  frontend/src/lib/api/schemas.gen.ts
                   go generate ./cmd/awg-manager     cd frontend && npm run gen:api
```

Run both commands in that order and commit what they produce. Editing
`schemas.gen.ts` by hand, even correctly, leaves `swagger.yaml` stale and
fails the "Swagger drift" check; the frontend check then fails too because
the file is fresher than its own source. The read-only key scope shipped
with exactly this mistake and cost a CI round trip.

## Mirror the REST handler's side effects

- Publish the same invalidation event the HTTP handler publishes
  (`l.publish(events.Resource…, "mcp-<action>")`), or an open web tab keeps
  showing pre-MCP state.
- Log through the scoped logger for that area with `(MCP)` in the message, so
  the journal filtered by group shows MCP-driven changes beside web-driven ones.
- Reuse the daemon's existing instance of a service (the delay checker, the
  router service). A second instance has its own view of drafts and doubles
  background probes.

## Testing

`internal/mcp` and `internal/mcp/mcptest` run natively:

```
go test ./internal/mcp/ ./internal/mcp/mcptest/ -count=1
```

`internal/mcp/localdeps` does not build on macOS (transitive Linux syscalls).
Typecheck it with `GOOS=linux go vet ./internal/mcp/...` and RUN it in a
container:

```
docker run --rm -v "$PWD":/src -w /src golang:1.27rc1 go test ./internal/mcp/... -count=1
```

Do not report the adapter tests as passing if the container did not run. Say
they were typechecked only.

The darwin build is enforced: CI job `mcp-portable` cross-builds
`cmd/mcp-dev` for darwin/arm64. So anything the fake imports must build
there. `internal/managed` does not (it pulls in `syscall.Uname` through
osdetect); a rule the fake must share with the service goes into a leaf
package the service itself uses — `internal/managed/peerip` is the pattern.
Check with `GOOS=darwin go build ./cmd/mcp-dev` before adding an import.

## Tell the model about the tool

Descriptions are read by a model choosing among 40 tools. Say what question the
tool answers, what its result does NOT mean, and which tool to call next.
Update `Instructions` in `server.go` when a tool changes a global rule (a new
draft semantics, a new kind of key). Add a CHANGELOG entry under `Unreleased`
in the project's Russian style: what changed and why the old behaviour was
wrong.

## Checklist before committing

- [ ] Tests in both packages, written first, seen failing for the right reason
- [ ] Implementation broken once to prove a test catches it
- [ ] Fake cites the real service line it mirrors
- [ ] Ids validated before Deps; IPs canonical
- [ ] Output says what its ambiguous values mean; pages carry totals and flags
- [ ] Annotation matches reality; scope list and catalogue test updated
- [ ] No secret crosses the boundary (grep test)
- [ ] Invalidation event published, scoped log line with `(MCP)`
- [ ] Persisted-format version bumped if a stored field was added
- [ ] API DTO changed → `go generate ./cmd/awg-manager` then `npm run gen:api`, both outputs committed
- [ ] Adapter tests run in the container, not only typechecked
- [ ] Description names what the result does not mean; CHANGELOG entry
