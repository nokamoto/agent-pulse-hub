---
type: Design
title: GitHub review request trigger
description: Poll direct review requests through bounded GitHub search and retain notification progress through the public runtime-state interface.
sources:
  - id: requirements
    resource: requirements/github-review-request.md
  - id: bundled
    resource: requirements/bundled-plugins.md
  - id: runtime-state
    resource: design/trigger-runtime-state.md
  - id: protocol
    resource: design/plugin-v1.md
  - id: search
    resource: https://docs.github.com/en/rest/search/search
  - id: ghes-search
    resource: https://docs.github.com/en/enterprise-server@3.19/rest/search/search
---

# GitHub review request trigger

## Context and scope

This design fulfills [GitHub review request requirements](../requirements/github-review-request.md).
The bundled trigger observes open pull requests requesting the authenticated
user's review and emits their URLs. It uses the existing input role and
`runtime_state_v1` capability of [plugin protocol v2](plugin-v1.md).
The hub continues to own routing, one-attempt delivery, registration storage,
and [coordinated checkpoints](trigger-runtime-state.md).

Observation is limited to GitHub's search coverage. It is not an event-history
feed or a guarantee of visibility into every repository. Team-only requests,
repository filters, webhook receivers, delivery retries, crash recovery, and
GitHub-specific hub changes are outside scope. A direct assignment caused by
team automation is a direct request and therefore matches.

## Architecture and artifacts

The plugin owns host validation, credential selection, authenticated identity,
GitHub HTTP requests, observation differences, and progress encoding. The hub
does not interpret GitHub URLs, credentials, or checkpoints. Each watch owns
its observation state; registrations never share notification progress.

| Path under `plugins/triggers/github-review-request/` | Responsibility |
| --- | --- |
| `main.go` | Assemble the existing v2 framework and plugin-specific adapters. |
| `internal/domain/` | Watch, observation, occurrence, and checkpoint validation and transitions. |
| `internal/application/` | Coordinate polling and ordered protocol messages through consumer-owned HTTP, credential-process, and clock boundaries. |
| `internal/adapters/` | GitHub HTTPS, saved `gh` authentication, and public Session integration. |
| `readme.md` | Operator prerequisites, configuration, limits, and recovery. |
| `examples/daemon.json`, `examples/watch.json` | Daemon input configuration and host/interval watch arguments with an operator-selected action. |

Implementation introduces the complete bundled artifact set together. Delivery
verifies its instructions and updates the root README entry points. No new
plugin-specific skill is needed. Reuse existing framework dependencies; the
GitHub adapter uses Go's standard HTTP and JSON libraries, not a new SDK.
Package and helper names within the permitted layers remain implementation choices.

## Configuration and authentication

### Watch arguments

`watch_args` is an object with only the following optional fields. Reject
unknown fields, wrong types, and invalid values before positive watch acceptance.

| Field | Default | Contract |
| --- | --- | --- |
| `host` | `github.com` | Lowercase ASCII DNS hostname, at most 253 bytes; normalize ASCII case. No scheme, user information, path, query, fragment, port, trailing dot, or IP literal. |
| `poll_interval` | `60s` | Positive Go duration string; fractional durations are allowed. Reject zero, negative, malformed, or overflowing values. |

Use `https://api.github.com` for github.com and `https://<host>/api/v3` for
GHES. This profile uses HTTPS on port 443 and normal system certificate trust;
there is no insecure TLS or custom API-base argument. The selected host is
trusted operator configuration, never derived from an API response.
No repository or organization field is accepted.

The input instance inherits the daemon's process environment. The existing
daemon configuration has no per-plugin environment field. For different hosts
with different environment tokens, start separate daemon processes with
separately supplied environments, distinct state roots and distinct loopback
control addresses, then register each host on its corresponding daemon.
Each daemon needs its own configured input and action. When both environment
token variables are absent, one daemon can instead use separate registrations
with host-specific saved `gh` authentication. Never use a daemon carrying one
host's token to register another host. Do not place tokens in watch arguments
or commit resolved secrets in daemon examples.
Changing an instance's identity requires an explicit progress reset or a new
registration; it does not silently reinterpret existing progress.

### Credential selection and identity

Select the first nonempty `GH_TOKEN`, then `GITHUB_TOKEN`. Empty means zero
bytes; do not silently trim or reinterpret a nonempty invalid token as absent.
An environment token never invokes or requires `gh`. Retain the selected
credential only in process memory and send it only to the configured API origin.

If both are empty, locate trusted `gh` on PATH and execute the argument array
`auth token --hostname <host>`, without a shell. Remove `GH_TOKEN`,
`GITHUB_TOKEN`, `GH_ENTERPRISE_TOKEN`, and `GITHUB_ENTERPRISE_TOKEN` from that
child's environment; remove `GH_HOST` and set `GH_PROMPT_DISABLED=1`. Preserve
the operator's CLI configuration/keychain access. Use only the explicit hostname.
Bound the process to three seconds and stdout/stderr to 64 KiB each; require
exit zero and one nonempty token after removing the CLI's terminal line ending.
Do not echo stdout, stderr, or command error text that can contain credentials.
Missing executable, timeout, unavailable saved login, and empty output are
actionable prerequisite errors. There is no interactive login or automatic
credential-source switch.

Within an overall eight-second watch-validation budget, use the selected token
for `GET /user` and obtain an exact positive uint64 `id` and nonempty `login`.
Reject watch acceptance on failed identity resolution, including timeout,
authentication, permission, or rate-limit failure. This fits the public
ten-second watch deadline and prevents a saved identity from being used without
checking the current credential. Operators retry registration/startup after
resolving that error. Do not fetch the complete search during watch validation.
Readiness itself performs no GitHub requests.

Thereafter a watch retains that identity and credential for its Session.
Credential rotation takes effect on restart. A rejected selected token is an
error even when a lower-priority source would work. Never infer a user's
identity from a variable name: a built-in Actions `GITHUB_TOKEN` or an
installation token that cannot resolve a user is not supported.

### Service and token compatibility

The API profile targets github.com and **GHES 3.19.x**, using
`Accept: application/vnd.github+json` and `X-GitHub-Api-Version: 2022-11-28`.
Other GHES releases are outside the declared profile until reviewed. The
required capabilities are authenticated `/user`, REST issue/PR search,
`user-review-requested:@me`, pagination, `total_count`, and `incomplete_results`.
These are documented in the
[GHES 3.19 search reference](https://docs.github.com/en/enterprise-server@3.19/rest/search/search)
and [search qualifiers](https://docs.github.com/en/enterprise-server@3.19/search-github/searching-on-github/searching-issues-and-pull-requests).
An unsupported endpoint, qualifier, or API version fails visibly; do not
substitute a broader team-inclusive query. A response's version header is
diagnostic evidence, not proof that its configured installation is compatible.

Use a user credential with access to the intended repositories. Classic PATs
or OAuth tokens need `repo` for private repository search; this scope includes
write capabilities although this plugin performs only reads. Public-resource
read access does not require that scope. Fine-grained PATs and GitHub App user
access tokens must be accepted by `/user` and issue search; those endpoints
document no additional fine-grained permission requirement. Their selected
repository/resource-owner scope and organization approval/SSO policies still
limit visibility. No installation-token or GitHub App provisioning workflow is
added. See [authenticated user](https://docs.github.com/en/rest/users/users#get-the-authenticated-user),
[search](https://docs.github.com/en/rest/search/search#search-issues-and-pull-requests),
and [OAuth scopes](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/scopes-for-oauth-apps).

Documentation establishes an implementation target, not real-service evidence.
Both hosts require the separate delivery checks below. In particular, GHES
environment availability and exact tested patch version remain an explicit
approval decision in the open-question table.

## Retrieval and supported coverage

Issue the URL-encoded query
`is:pr is:open user-review-requested:@me` to `GET /search/issues`, with
`per_page=100`, `sort=created`, `order=asc`, and increasing `page` starting at 1.
Use the direct-user qualifier, not `review-requested`, which also includes
team requests under GitHub's documented search behavior. Search responses
provide issue IDs and `html_url`; do not confuse an issue ID with a PR's
number or the distinct ID returned by the pull-request detail API.

Collect at most **1,000 distinct matches** per observation. Require every page
to succeed, `incomplete_results=false`, a consistent integer `total_count`
between zero and 1,000, no duplicate IDs, and exactly that many distinct valid
records across the expected pages. Each record must identify an open PR,
have an exact positive uint64 issue ID and an HTTPS PR `html_url` on the selected
web host with no user information, query, fragment, or nondefault port. Require
the `/owner/repository/pull/<positive-number>` path shape and the public
8,192-byte context bound. Treat malformed data, missing pages, duplicate IDs,
count changes, and result overflow as incomplete retrieval. Do not follow
arbitrary response URLs or `Link` targets with the credential; construct page
requests locally against the selected API origin. Reject HTTP redirects.

Only a complete collected observation may reconcile presence or supply new
events. Incomplete retrieval performs no observation-based additions, removals,
or event generation. Previously scheduled emission and delivery notices remain
independent and continue to update their existing occurrences. Recovery with
the same matches produces no fabricated disappearance or repeat notification.

GitHub also limits search to **4,000 repositories selected by its search
service**. The API does not document a reliable flag for exceeding that
repository coverage. Therefore the plugin cannot certify full visibility over
all repositories accessible to a credential, even when `total_count` is small
and `incomplete_results` is false. Report this fixed coverage limitation at
watch startup and put it beside the normal usage steps: “Search covers at most
4,000 GitHub-selected repositories; coverage beyond that limit cannot be
detected or guaranteed.” This is an unconditional coverage notice, not an
assertion that the credential currently exceeds the limit. A complete
observation means complete within this declared search coverage, as defined
by the requirement's complete-observation term. Known result-count and partial
result limits are errors, never silent truncation. Operators needing exhaustive
coverage beyond this search domain are outside this profile.

Search is eventually indexed, and paginated results are not an atomic snapshot.
The checks above detect reported incompleteness and obvious page churn; they
cannot prove absence of all concurrent changes or delayed indexing. Observed
disappearance means disappearance from a complete search observation, not
proof of an instantaneous GitHub state transition. There is no historical
capture or end-to-end latency guarantee.

For N matches within capacity, a successful poll uses
`max(1, ceil(N / 100))` search requests: one for 0–100 matches and ten for
901–1,000. It makes no per-repository or per-PR detail requests. The 4,000
repository scope is server-side search scope, not 4,000 client requests.
Identity resolution adds one `/user` request per watch start. With the default
interval this is approximately 1–10 search requests per minute per watch,
reduced by request duration. Watches do not share retrieval results; multiple
registrations multiply this load, including watches using the same credential.
GitHub documents a usual authenticated search limit of 30 requests per minute;
other clients can consume the same allowance and secondary limits also apply.
Do not treat that figure as a reserved plugin quota. Operators can increase
the interval to reduce load. Rate-limit responses control the actual retry
schedule below. This per-watch cost and lack of cross-watch sharing are
accepted tradeoffs of the selected retrieval strategy.

### Scheduling and failures

Start the first observation after positive `watch_result` is sent. Serialize
observations per watch. After each completed attempt, wait the configured
interval before the next; never queue missed ticks. Bound each HTTP request
to ten seconds, each response to 8 MiB, and each observation to 120 seconds.
These are retrieval resource bounds, not notification deadlines. Oversized or
timed-out responses are incomplete failures. Keep HTTP work off the ordered
protocol receiver so checkpoints remain responsive.

For network/timeouts and 5xx errors, retry retrieval after the greater of the
interval and exponential delay starting at one second and capped at five
minutes. For 429 or rate-limited 403, also honor valid `Retry-After` (delta or
HTTP date) and exhausted `X-RateLimit-Reset`; never retry before the latest
applicable server time. Without guidance, rate-limit delay is at least one
minute. Successful observations reset backoff. Retry incomplete collections
after at least the interval. Authentication/permission/unsupported-API errors
retain state and report a watch error; probe again no faster than the greater
of the interval and five minutes, with no credential fallback. A later success
recovers monitoring; environment credential changes require restart.

Classify diagnostics as authentication, permission, network, service,
rate-limit, unsupported API, incomplete retrieval, or capacity. Include the
registration, selected host, and bounded safe status/reason. Do not print
authorization headers, raw response bodies, CLI output, checkpoint contents,
or PR URLs in error diagnostics. Stop cancels HTTP and timers immediately;
quiescing does not wait for an observation timeout.

## Occurrences, delivery, and checkpoints

### Observation transitions

A request occurrence is a PR's uninterrupted presence across complete
observations. At the first complete observation, create an unsent entry for
each match. Preserve entries for IDs still present, remove absent IDs, and
create new entries for newly present IDs. An incomplete observation performs
none of these transitions. Assign memory-only occurrence generations so a
late notice for an older occurrence cannot modify a later occurrence of the
same PR. Keep event-to-occurrence correlations until terminal notices or
Session closure; bound emission to one outstanding event per watch.

Emit each unsent entry once as `state_event` with context exactly its `html_url`
and the public monotonically increasing event counter. Mark the entry attempted
before sending. Do not resend after failed, unknown, or discarded outcomes.
Terminal notices release the emission slot. Only an `accepted` notice changes
the matching current occurrence to confirmed. A notice for an occurrence
already removed has no effect on current progress. A send/stream failure ends
the Session through the existing lifecycle. Counter exhaustion fails visibly.

After a complete absence, the same ID's reappearance is a new occurrence and
is eligible again. An unobserved cancellation/re-request, including while
stopped, does not create a new occurrence. Multiple observations may proceed
while waiting for delivery; do not block checkpoint processing on delivery.
This preserves first-use and observed-transition behavior without introducing
automatic retries for an earlier attempt.

### State representation

Use this exact state envelope, with no additional fields:

```json
{"schema":"github-review-request-v1","payload":{"host":"github.com","user_id":"123","entries":[["456",2]]}}
```

`host` is the canonical selected web hostname. `user_id` and each issue ID are
canonical positive uint64 decimal strings. `entries` is sorted by numeric issue
ID, unique, and has at most 1,000 rows. Each row is exactly `[issue_id,status]`:
integer `0` means observed but unsent, `1` means attempted without confirmed
acceptance, and `2` means confirmed acceptance. Status 1 includes pending,
failed, unknown, and discarded attempts; it never means successful notification.
All present entries suppress duplicate attempts in the running occurrence,
except status 0, which is eligible for its first attempt. URLs, credentials,
login names, interval, event counters, and in-flight correlations are not saved.

At 20 digits per ID each row occupies at most 26 bytes, plus one separator;
1,000 rows occupy at most 27,001 bytes including the outer array. With a
253-byte ASCII host, 20-byte user ID, fixed keys and schema, the complete compact
envelope remains below 28,000 bytes. Measure the actual compact envelope with
the common encoder before returning it, including the 32,768-byte limit;
overflow is a checkpoint failure, never truncation. This leaves margin without
depending on compression, hashes with collision risk, or unbounded URLs.

On restoration validate exact schema/fields/types, bounds, ordering, unique
IDs, and status values, then require host and `/user` ID to match the watch.
Reject incompatible state before starting the watch; never silently reset.
With null state, all initial matches are unsent. With saved state, wait for
the first complete search to obtain current URLs and reconcile presence.
Retain status 2 to suppress confirmed occurrences. Retain status 1 to avoid
retrying unfinished attempts; do not claim they were delivered. Status 0 may
make its first attempt only if still present. Newly present IDs become status
0, and absent IDs are removed. The plugin never owns state files.

Process `delivery_notice`, observation commits, event emission, and checkpoint
extraction in one ordered per-watch state coordinator. A checkpoint applies
all preceding notices before taking its snapshot. For `quiesce:true`, cancel
retrieval/timers, prevent further emission and observation commits, then
snapshot promptly; remain quiescent until Session closure. Late HTTP results
cannot change a quiesced snapshot. Existing common save-failure continuation
and final-stop fences remain authoritative. Saved status 2 plus successful
normal shutdown establishes confirmed restart suppression; status 1 carries
no added delivery/recovery guarantee.

## Specification documents and operating procedure

This document is the authoritative plugin configuration, retrieval, state,
and compatibility specification for implementers and maintainers. No separate
GitHub specification is deferred. [Plugin v2](plugin-v1.md) owns the unchanged
wire contract; [runtime state](trigger-runtime-state.md) owns checkpoint
scheduling/storage/stop; [MVP](mvp.md) owns routing and daemon configuration.
The bundled README/examples derive from these contracts and cannot extend them.

For continued developer and operator use:

1. Build the plugin with `go build -o <output> ./plugins/triggers/github-review-request`.
   Supply a trusted PATH only when using saved `gh` authentication. Configure
   certificate trust for GHES using the operating system's normal trust store.
2. Configure the executable as `role: "input"` in the existing `plugins` array,
   together with an existing action. Supply the chosen host's credentials to
   the daemon process environment. For different host tokens start separate
   daemons, each with a different `--state-dir` and `--address 127.0.0.1:<port>`;
   use that same address for its register and stop commands. Saved host `gh`
   logins can share a daemon only with both environment token variables absent.
3. Register with `{"host":"github.com","poll_interval":"60s"}` (or the
   GHES hostname) as watch arguments and the chosen action's destination
   arguments. No repository list is needed. Verify the startup coverage notice.
4. Observe one URL event for each initial direct match, then newly observed
   occurrences. Use normal daemon stop and wait for process exit. Verify final
   checkpoint/save success before relying on restart continuity.
5. Restart with the same registration, host, and user identity. For deliberate
   identity changes or invalid state use the existing
   [offline recovery procedure](windows-operations.md#trigger-runtime-state-recovery),
   preserving the state tree first. Reset relinquishes previous suppression;
   initial matches may notify again. Do not edit live or individual state rows.

Examples use POSIX-shell notation where a shell is necessary; it is not an OS
requirement. Delivery supplies complete runnable daemon/registration examples
using these contracts and inspects credential isolation for multiple hosts.

## Decisions and alternatives

| Decision | Alternatives | Rationale and tradeoff |
| --- | --- | --- |
| Bounded REST search with direct-user qualifier | Enumerate `/user/repos` and every PR; GraphQL; webhook/history | Search reaches non-affiliated public repositories and is documented on both target hosts. Repository enumeration does not enumerate every accessible public repository; GraphQL adds another capability surface; history/webhooks exceed scope. Search coverage limits remain explicit. |
| One compact status row per current issue ID | Store URLs or two complete ID sets; compression; hashes | Exact bounded IDs fit the common checkpoint. Status separates confirmed success from attempted work without doubling worst-case storage. |
| One outstanding event per watch | Unbounded burst; concurrent delivery map | Bounds correlation memory and simplifies notice ordering; a large initial set may take many delivery round trips. No latency guarantee is added. |
| Saved-login CLI only after absent environment tokens | Always invoke `gh`; CLI API calls; enterprise-token precedence | Preserves the approved precedence and supports token users without a CLI. Sanitizing the child environment prevents implicit enterprise-token substitution. |
| TLS trust and address routing substituted only in test processes | Production insecure/test endpoint flags; a new proxy framework | Exercises the real HTTP/TLS adapter without adding product configuration or shared infrastructure. Shipping executable identity remains a separate source/build and delivery concern. |

## Requirement coverage

All GRR IDs below refer to
[requirements/github-review-request](../requirements/github-review-request.md).

| Requirement | Acceptance | Design | Verification |
| --- | --- | --- | --- |
| FR-001, FR-002 | AC-001 | Watch host and credential/identity selection | U-AUTH; GRR-V01-AUTH; GRR-D01/D02 |
| FR-003, FR-005, FR-006 | AC-002, AC-003 | Retrieval and occurrence transitions | U-OBSERVE; GRR-V02-OBSERVATION; GRR-D01/D02 |
| FR-004 | AC-004 | Duration validation and scheduling | U-SCHEDULE; source inspection |
| FR-007, FR-008 | AC-005, AC-007 | Status rows, ordered notices, restore and quiesce | U-STATE; GRR-V03-RESTART; common TRS-V01/V02/V03; GRR-D01/D02 |
| FR-009 | AC-006 | All-or-nothing observations, diagnostic classes and backoff | U-RETRIEVE; GRR-V02-OBSERVATION |
| FR-010 | AC-008 | Artifacts, service profile, operating procedure | Artifact guardrail; documentation review; GRR-D01/D02 |
| NFR-001 | AC-001, AC-007, AC-008 | Origin binding, TLS, secret exclusions and child environment | U-AUTH/U-STATE/U-RETRIEVE; GRR-V01/V02/V03; source and example review |
| NFR-002 | AC-005, AC-008 | Go layers, existing protocol and common ownership | Source/contract review; GRR-V03-RESTART |
| NFR-003 | AC-008 | Separate evidence levels | Review of unit, assembled and delivery records |
| bundled-plugins FR-001–003 | bundled-plugins AC-001 | Complete role directory/artifacts | Existing artifact guardrail and delivery documentation review |

### Unit verification supplied by implementation

These are coverage groups, not Ginkgo integration case IDs. Use standard Go
tests and consumer-owned mocked external capabilities; no live HTTP, subprocess,
filesystem, or wall-clock sleeps belong in these unit matrices.

| Group | Concrete boundaries and expected results |
| --- | --- |
| U-AUTH | Host invalid forms and case normalization; both/one/empty tokens; rejected chosen source never falls back; CLI absent/error/timeout/output overflow and sanitized environment; `/user` malformed IDs and state identity mismatch; no secret markers in errors. |
| U-OBSERVE | Direct-query construction, open PR validation, exact URL context; A/A/empty/A produces two attempts; A/A across unobserved cancellation produces one; distinct IDs produce distinct attempts; same PR number in two repos stays distinct. |
| U-SCHEDULE | Controlled clock shows default 60s and configured positive duration after attempt completion; zero/negative/malformed rejected; no overlapping polls; server retry timestamps and exponential reset; quiesce cancels work. |
| U-RETRIEVE | 0/100/1,000/1,001 results; page failure, duplicate/count drift, incomplete flag, invalid IDs/URLs, redirect, body limit; 401/403/429/5xx/network errors; each preserves A and recovery A does not repeat; retry guidance is never violated. |
| U-STATE | Null versus compatible and corrupt/unknown state; all status values; accepted notice before checkpoint; late notice across absence/reappearance; status 1 never claimed accepted or retried; 1,000 maximum-width rows fit; oversize rejected; quiesce fences late HTTP results. |

### CI acceptance cases and selection

Use three integration scenarios to test wiring that isolated decisions cannot
establish. Put design-only cases and suite in
`cmd/github-review-request-verification/*_integration_test.go`, all with
`//go:build integration`, with local fixtures in its `testdata/` directory.
Implementation relocates these cases and fixtures to the new plugin root and
its `testdata/`, preserving IDs and expectations and removing the old suite.
The complete plugin implementation scope must pass all three cases.

| Stable case ID | Mapping | Concrete stimulus and expected results |
| --- | --- | --- |
| GRR-V01-AUTH | AC-001, NFR-001 | Launch public Session against a test-instrumented real plugin command. Set both token markers with `gh` absent: `/user` must receive GH_TOKEN only. Reject it while a valid lower source exists: negative watch result, no fallback. With both absent run a native `gh` fixture for GHES: exact hostname argv, all four token variables absent, saved marker used only on GHES. Missing auth is a bounded negative watch result; diagnostics contain no token marker. |
| GRR-V02-OBSERVATION | AC-002, AC-003, AC-006, NFR-001 | Through standalone public Session, a controlled HTTPS server offers two open direct PRs in two repositories and excludes team-only/other-user/closed/merged candidates according to the asserted direct query. Expect exact A and B URLs once; repeat unchanged, observe A absent, then A present and expect one new A. After A is confirmed, fail a page or return incomplete data, then recover unchanged: no repeat. A rate-limit response with Retry-After delays the next request. Inspect query, HTTPS host, event fields, and safe diagnostics; send terminal notices to release each emission slot. |
| GRR-V03-RESTART | AC-005, AC-007, NFR-002 | Use the actual hub/client, instrumented plugin command and model output with controlled GitHub. Register two distinct destinations and confirm A independently for each; observe successful checkpoints, stop normally and wait for exits. Restart both retained routes with A+B: each destination receives only B as a new event. With the daemon and configured plugins stopped, replace one runtime envelope payload by an incompatible schema: startup fails without fallback or output delivery. Preserve and remove the temporary state tree after all processes exit. |

Keep the full failure/scheduling/schema matrix in unit tests instead of
repeating it through processes. Combine direct matching and partial-retrieval
recovery in V02. Reuse common
[TRS-V01/V02/V03](trigger-runtime-state.md#verification-strategy) for reset,
association, core save failure and restoration rules; V03 adds the actual
GitHub state adapter and identity binding. U-STATE verifies plugin-specific
save-independent polling and invalid progress. Do not duplicate the common
filesystem fault harness or add a real-service CI matrix. Common checks alone
do not prove this plugin's restart behavior.

For V01/V02 use the existing public go-plugin launch/dispense/Session recipe,
with opaque caller IDs, rather than a hub or a second external integration.
For V03 use existing daemon/client lifecycle and model-output artifact patterns.
The real plugin command consists of its shipping composition and application
layers, compiled into an integration test executable with a test-only helper
entry. That entry sets a trusted ephemeral CA and local address routing on the
standard HTTP transport, then calls the production command entry. Certificates
carry the actual selected API hostname and undergo normal TLS verification.
Only GitHub HTTPS responses, trust roots/address routing, clock where needed,
and the credential CLI are controlled; routing, state, HTTP parsing, domain
logic and public gRPC remain real. Do not set `InsecureSkipVerify` or introduce
production endpoint flags. This establishes assembled behavior of instrumented
commands, not a claim of live compatibility or identical shipping executable bytes.

The native Go `gh` fixture records safe argv/environment observations and
returns synthetic tokens. Use explicit environment arrays and temporary
directories; no login or developer credential store is touched. Build fixture
executables once per suite using `go env GOEXE` for suffixes. Listen only on
loopback ephemeral ports. Bound each child lifetime and output; cancel, kill
if needed, wait for exit, close servers, and then remove temporary directories.

The minimal design feasibility probe exercises verified local HTTPS with
hostname routing and the native CLI environment boundary. It must contain no
product substitute presented as acceptance. Existing public framework and
common runtime-state suites supply the established process/protocol pattern;
static Pending cases defer product wiring, not selection or method.

Budget: build each fixture/test command once per suite, with at most 30 seconds
for warm-dependency build/setup, V01/V02 at most 15 seconds each, V03 at most
45 seconds, and final cleanup at most five seconds. The total is 110 seconds,
leaving ten seconds within the existing 120-second scoped suite deadline;
at most one TLS fixture and, for V03, one daemon plus configured plugin processes
per phase. A cold dependency download is separate setup evidence. Reuse the
existing Go integration job and its dependencies; no CI, dependency-version,
shared harness, timeout, or verification-judgment changes are required. Record
actual probe costs and any unavailable environment in the PR, never as product
acceptance. The proposed case budget requires design review agreement.

Run separately from the repository root:

```sh
go run build/mage.go -d build -w . test
go run build/mage.go -d build -w . integrationAll
go test -tags=integration ./cmd/github-review-request-verification -v
go run build/mage.go -d build -w . guardrails
```

After case relocation the focused command addresses
`./plugins/triggers/github-review-request` instead. The generic integration
target discovers the suite automatically. Compile and enumerate all three
static Pending cases during design; Pending, skipped, missing or filtered cases
are not implementation acceptance.

The scoped inventory is
`cmd/github-review-request-verification/testdata/github-review-request-cases.json`.
Relocate its package path with the suite, preserving its three IDs. Also run
the existing common runtime-state inventory separately for reused TRS coverage;
neither inventory replaces the other.

### Delivery quality checks

| Check | Mapping | Preconditions, actions and expected result | Evidence and evaluator |
| --- | --- | --- | --- |
| GRR-D01 | AC-001/002/005/008 | A maintainer uses github.com with a real user token and, separately, saved host `gh` auth. Register the shipping plugin, create direct requests in two controlled repos, a team-only request, another user's request and closed/merged cases. Observe only direct open URLs, no unchanged repeats, observed absence/reappearance, then accepted A plus successful normal restart with fresh B. Repeat selected-token rejection to establish no fallback. | Delivery maintainer records revision, host, credential type/permissions without secret, redacted config, exact stimulus, URL events, action acceptance and final save/exit records. |
| GRR-D02 | AC-001/002/005/008, Q-001 | Repeat D01 on an available GHES 3.19.x installation with known patch version and controlled repositories/users/team. Configure separate daemon environments, state roots and control addresses for github.com and GHES tokens; verify host identity and no token transfer. Separately exercise saved host logins with environment tokens absent. | Delivery maintainer records exact version/API profile, environment owner, same observations, and separate result. No mock response or github.com success substitutes for GHES. |

Real-service execution may be deferred, but remains visibly unverified until
these checks run. Review finalized README/examples for all FR-010 topics,
normal recovery, exact capacity and coverage warnings, and secret exclusions.
The service-free model walkthrough retains its independent scope. Passing this
design's probes or common runtime tests does not satisfy either delivery check.

## Change impact and open decisions

This adds a bundled input profile and its design-governed verification only.
It changes no hub contract, old plugin behavior, shared tooling, or storage
format. A participating host is required; old hosts without the optional
runtime-state extension are unsupported. There is no old GitHub checkpoint to
migrate. Rollback to a binary lacking this schema must fail visibly on restore;
use the established offline restore/reset procedure rather than ignoring state.

| Question or risk | Impact | Owner | Resolve before | Status |
| --- | --- | --- | --- | --- |
| Q-001: GHES environment and tested patch version | Documentary 3.19.x API support does not establish service compatibility | Responsible human and delivery maintainer | Design approval for environment plan; delivery for execution | API target and permissions specified; actual environment unassigned and real-service verification deferred. |
| Q-002: Search domain and capacity | At most 1,000 matches within GitHub-selected 4,000-repository coverage; exceeding repository coverage cannot reliably be detected | Design reviewer | Design approval | Bounded search and exact-ID status encoding selected with the explicit coverage limits; per-watch API cost accepted. |
| Eventual indexing and non-atomic pagination | Brief changes and some concurrent churn are not observable | Operator | Usage | Accepted observation-model limit; detect reported/observable incompleteness and preserve state. |
| Instrumented CI versus live services | Controlled TLS/process success cannot certify deployed GitHub behavior | Delivery maintainer | Delivery completion | D01/D02 remain required and separate. |

Unassigned GHES environment planning prevents an unconditional design-readiness
claim. Deferring live execution does not remove that decision or waive required
delivery acceptance.
