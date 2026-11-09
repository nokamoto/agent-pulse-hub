---
type: Design
title: GitHub review request trigger
description: Establish a silent baseline for direct review requests and retain notification progress through the public runtime-state interface.
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
user's review. It records the first complete observation of a fresh watch
silently and emits URLs for matches newly present in later observations.
It uses the existing input role and
`runtime_state_v2` capability of [plugin protocol v2](plugin-v1.md).
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

The bundled guide and examples describe setup, baseline timing, and recovery
for operators. Delivery verifies them and the root README entry points.
The GitHub adapter uses Go's standard HTTP and JSON libraries and the
existing plugin framework. Package and helper names within the permitted
layers remain implementation choices.

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

Validate credentials, resolve `/user` and apply compatible state before positive
`watch_result`, then retain a dormant watch. Start its first search observation
only after consuming `activate_watch` under the
[public activation contract](plugin-v1.md#runtime-state-v2-activation). Do not
poll, reconcile, mark attempts or emit while dormant. The hub owns the complete
restoration/admission barrier; the plugin owns per-watch idempotent activation
and permanent quiescence. A standalone caller prepares its receiver/correlations
after acceptance, sends the same grant, then observes events. Duplicate grants
start no extra poller; a late grant cannot resume quiescence. Serialize
observations per watch. Measure the configured interval from the time the
Session processes the previous retrieval result, whether successful or failed.
The interval runs while pending events are being emitted. Start the next
retrieval when the interval and any failure backoff have elapsed and all
eligible entries from the last complete observation have been emitted. The
Session continues processing notices and checkpoints during the wait. Never
queue missed ticks. Bound each HTTP request
to ten seconds, each response to 8 MiB, and each observation to 120 seconds.
These are retrieval resource bounds, not notification deadlines. Oversized or
timed-out responses are incomplete failures. Keep HTTP work off the ordered
protocol receiver so checkpoints remain responsive.

For network/timeouts and 5xx errors, retry retrieval after the greater of the
interval and exponential delay starting at one second and capped at five
minutes. Classify HTTP 429 as rate limiting. Classify HTTP 403 as rate limiting
when it carries a nonempty `Retry-After`, `X-RateLimit-Remaining: 0`, or a
bounded JSON error message indicating a rate limit or abuse detection.
Classify other HTTP 403 responses as permission failures. For rate limiting,
honor valid `Retry-After` (delta or
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
observations. A watch with no saved progress starts with its baseline unset.
Its first complete observation establishes the baseline, even if empty.
Record every match in that observation as a silent baseline entry, including
requests made after registration and before the observation. On each later
complete observation, preserve entries for IDs still present, remove absent
IDs, and create unsent entries for newly present IDs. Validate the entire
observation before committing its baseline or entry changes. Failed or
incomplete observations leave both unchanged.

Assign memory-only occurrence generations so a
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

Emit all eligible entries from a complete observation before starting the
next retrieval, so a later absence cannot erase an occurrence before its first
attempt. The final outstanding event can remain in flight during the next
observation because it has already been emitted. A restored status 0 lacks
a current URL and becomes eligible only if the first complete observation
still contains that ID. A quiescing checkpoint can end polling while entries
still await emission. Persist their current status and apply the ordinary
restore rules at the next start.

After a complete absence, the same ID's reappearance is a new occurrence and
is eligible again. An unobserved cancellation and re-request, including while
stopped, does not create a new occurrence.
After the first complete observation commits in each activation, emit one
informational `github_review_request_first_complete_observation` diagnostic
with the registration ID, selected host, `match_count`, and a `baseline_created`
boolean indicating whether this observation established the baseline. Keep
PR IDs, URLs, credentials, and response bodies out of the diagnostic.

### State representation

Write this exact state envelope, with no additional fields:

```json
{"schema":"github-review-request-v2","payload":{"host":"github.com","user_id":"123","baseline_established":true,"entries":[["456",3],["789",2]]}}
```

`host` is the canonical selected web hostname. `user_id` and each issue ID are
canonical positive uint64 decimal strings. `entries` is sorted by numeric issue
ID, unique, and has at most 1,000 rows. The boolean `baseline_established`
is false only while waiting for the first complete observation and requires
an empty `entries` array. True with an empty array means the baseline is
established and the most recent complete observation has zero matches.

Each row is exactly `[issue_id,status]` with one of these integer values.

| Status | Meaning while the ID is present |
| --- | --- |
| `0` | Observed and eligible for its first attempt once its current URL is available. |
| `1` | Attempted without confirmed acceptance, including pending, failed, unknown, and discarded outcomes. |
| `2` | Confirmed action acceptance. |
| `3` | Present at baseline and suppressed until a complete observation establishes absence. |

Only status 0 is eligible to emit. URLs, credentials, login names, interval,
event counters, and in-flight correlations are kept out of saved progress.

At 20 digits per ID each row occupies at most 26 bytes, plus one separator;
1,000 rows occupy at most 27,001 bytes including the outer array. With a
253-byte ASCII host, 20-byte user ID, fixed keys and schema, the complete compact
envelope remains below 28,000 bytes. Measure the actual compact envelope with
the common encoder before returning it, including the 32,768-byte limit;
overflow is a checkpoint failure, never truncation. This leaves margin without
depending on compression, hashes with collision risk, or unbounded URLs.

### Restore, upgrade, and reset

On restoration validate the exact fields and types for the declared schema,
including the boolean and empty-array invariant in v2, bounds, ordering,
unique IDs, and allowed statuses. Require the saved host and `/user` ID to
match the watch. Reject malformed, incompatible, or identity-mismatched state
before positive watch acceptance. The hub owns all state files.

| Restored input | State before the first complete observation |
| --- | --- |
| Null, including a new registration or an explicit offline progress reset | Baseline unset, no entries. |
| Valid `github-review-request-v2` | Restore the saved baseline flag and entries exactly. |
| Valid `github-review-request-v1` from the version that notified initial matches | Restore with the baseline established, even when the saved array is empty. Read the v1 payload containing exactly `host`, `user_id`, and `entries`, with statuses 0–2. Preserve each saved status. |

Every checkpoint written by the new reader uses v2, including checkpoints
taken before a complete observation and those produced after restoring v1.
With an established baseline, wait for the first complete observation to
obtain current URLs and reconcile against the restored set. A present status
0 may emit its first attempt. Present statuses 1, 2, and 3 stay quiet. Newly
present IDs become status 0, and absent IDs are removed.

Process `delivery_notice`, observation commits, event emission, and checkpoint
extraction in one ordered per-watch state coordinator. A checkpoint applies
all preceding notices before taking its snapshot. For `quiesce:true`, cancel
retrieval/timers, prevent further emission and observation commits, then
snapshot promptly; remain quiescent until Session closure. Late HTTP results
cannot change a quiesced snapshot. Existing common save-failure continuation
and final-stop fences remain authoritative. Successfully saved statuses 2 and
3 preserve suppression through a normal restart. Status 1 carries no added
delivery or recovery guarantee.

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
2. Configure the executable as `role: "input"` in the existing `plugins` array
   together with an existing action. For token authentication, set the
   selected host's token in the daemon environment. Use separate daemons for
   different host tokens, with distinct `--state-dir` and
   `--address 127.0.0.1:<port>` values. Use the corresponding address for
   register and stop commands. For saved host `gh` authentication, leave both
   environment token variables empty. Those host logins can share a daemon.
3. Register with `{"host":"github.com","poll_interval":"60s"}` (or the
   GHES hostname) as watch arguments and the chosen action's destination
   arguments. No repository list is needed. Verify the startup coverage notice.
4. For a fresh watch, wait for the first-complete diagnostic. Requests in
   that observation form the silent baseline. Each later newly observed
   occurrence emits its URL. Use normal daemon stop and wait for process exit.
   Verify final checkpoint/save success before relying on restart continuity.
5. Restart with the same registration, host, and user identity. For deliberate
   identity changes or invalid state use the existing
   [offline recovery procedure](trigger-runtime-state.md#offline-reset-deletion-and-recreation),
   preserving the state tree first. After a reset, the first complete
   observation establishes a new silent baseline. Do not edit live or
   individual state rows.

Examples use POSIX-shell notation where a shell is necessary; it is not an OS
requirement. Delivery supplies complete runnable daemon/registration examples
using these contracts and inspects credential isolation for multiple hosts.

## Decisions and alternatives

| Decision | Alternatives | Rationale and tradeoff |
| --- | --- | --- |
| Bounded REST search with direct-user qualifier | Enumerate `/user/repos` and every PR; GraphQL; webhook/history | Search reaches non-affiliated public repositories and is documented on both target hosts. Repository enumeration does not enumerate every accessible public repository; GraphQL adds another capability surface; history/webhooks exceed scope. Search coverage limits remain explicit. |
| One compact status row per current issue ID | Store URLs or two complete ID sets; compression; hashes | Exact bounded IDs fit the common checkpoint. Status separates confirmed success from attempted work without doubling worst-case storage. |
| Explicit baseline flag and silent status in a new schema | Infer baseline from empty entries; save null before the first complete observation; mark baseline entries attempted | An empty array has two meanings and the hub persists non-null checkpoints. A dedicated status distinguishes baseline suppression from attempted delivery. A distinct schema makes old readers reject the new semantics even when the array is empty. |
| One outstanding event per watch and an emission gate before the next retrieval | Concurrent delivery map or an additional queue for disappeared, unsent matches | Keeps at most 1,000 current rows and one outstanding correlation. A slow delivery can delay polling while an eligible event still awaits emission. Once that event is sent, polling can resume before its outcome is known. |
| Saved-login CLI only after absent environment tokens | Always invoke `gh`; CLI API calls; enterprise-token precedence | Preserves the approved precedence and supports token users without a CLI. Sanitizing the child environment prevents implicit enterprise-token substitution. |
| TLS trust and address routing substituted only in test processes | Production insecure/test endpoint flags; a new proxy framework | Exercises the real HTTP/TLS adapter without adding product configuration or shared infrastructure. Shipping executable identity remains a separate source/build and delivery concern. |

## Requirement coverage

All GRR IDs below refer to
[requirements/github-review-request](../requirements/github-review-request.md).

| Requirement | Acceptance | Design | Verification |
| --- | --- | --- | --- |
| FR-001, FR-002 | AC-001 | Watch host and credential/identity selection | U-AUTH; GRR-V01-AUTH; GRR-D01/D02 |
| FR-003, FR-006 | AC-002 | Retrieval and URL-only emission | U-OBSERVE; GRR-V02-OBSERVATION; GRR-D01/D02 |
| FR-005 | AC-002, AC-003, AC-005, AC-006, AC-008, AC-009 | Complete observations, silent initialization, and absence/reappearance | U-OBSERVE/U-STATE; GRR-V02/V03; GRR-D01/D02 |
| FR-004 | AC-004 | Duration validation and scheduling | U-SCHEDULE; source inspection |
| FR-007, FR-008 | AC-003, AC-005, AC-007, AC-009, AC-010 | Versioned progress, ordered notices, restore, upgrade, and quiesce | U-OBSERVE/U-STATE; GRR-V03-RESTART; common TRS-V01/V02/V03; GRR-D01/D02 |
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
| U-OBSERVE | Direct query and open PR validation; incomplete empty response before a complete baseline containing A leaves the baseline unset, then A stays quiet. A/A/empty/A produces only the reappearance attempt. A/A across unobserved cancellation stays quiet for a baseline occurrence or preserves the single earlier attempt for a post-baseline occurrence. Later B and C in distinct repos get separate exact-URL events; the same PR number in different repos stays distinct. |
| U-SCHEDULE | No search or emission before grant, including on a dormant checkpoint and on quiescence before grant. Activation starts one initial search. Duplicate and late grants do not start polling for an active or quiesced watch. A controlled clock verifies the default 60s and a configured positive duration from retrieval result processing. Reject zero, negative, and malformed durations. Verify non-overlapping polls, server retry timing and exponential reset. With two eligible matches, hold the first event's notice through the interval and verify there is no next retrieval. Release its slot, emit the remaining event, and allow polling while its outcome is pending. Checkpoints remain responsive and quiescence cancels the wait. |
| U-RETRIEVE | Cover 0/100/1,000/1,001 results, page failure, duplicate/count drift, incomplete flag, invalid IDs/URLs, redirect, and body limit. Cover 401, 429, 5xx and network errors. Compare an ordinary 403 with each 403 rate-limit signal and verify the respective permission or rate-limit delay. Every failure preserves A, recovery does not repeat A, and retries respect server guidance. |
| U-STATE | Round-trip a quiescing v2 checkpoint before the first complete observation and separately one after a complete empty baseline. Restore each with A and expect silence and one A event, respectively (AC-009). Restore exact v1 payloads with an empty array and with present A/B/C at statuses 0/1/2. With new D in each next complete observation, expect D alone and A plus D, respectively; B and C stay quiet and the next checkpoint is v2 (AC-010). Check wrong identity, unknown schema, v1 status 3, v2 false flag with rows, and wrong flag types. Test notice-before-checkpoint ordering, late notices after reappearance, no retry while queued/in progress or after failure/unknown acceptance, 1,000 maximum-width rows, oversize rejection, and quiescence fencing late results. |

### CI acceptance cases and selection

Use the three existing case IDs directly in
`plugins/triggers/github-review-request/*_integration_test.go`, all with
`//go:build integration`. Keep local fixtures in that plugin's `testdata/`.
GRR-V01 remains runnable because authentication is independent of baseline
selection. GRR-V02 and GRR-V03 are static Pending until implementation
connects the revised behavior. All three cases must pass for implementation
completion.

| Stable case ID | Mapping | Concrete stimulus and expected results |
| --- | --- | --- |
| GRR-V01-AUTH | AC-001, NFR-001 | Launch public Session against a test-instrumented real plugin command. Set both token markers with `gh` absent: `/user` must receive GH_TOKEN only. Reject it while a valid lower source exists: negative watch result, no fallback. With both absent run a native `gh` fixture for GHES: exact hostname argv, all four token variables absent, saved marker used only on GHES. Missing auth is a bounded negative watch result; diagnostics contain no token marker. |
| GRR-V02-OBSERVATION | AC-002, AC-003, AC-006, NFR-001 | Use standalone public Session with a dormant checkpoint and duplicate activation. Assert the authenticated direct-user query on HTTPS. Return an incomplete empty result, then complete A, and confirm that the checkpoint records A at status 3 with no event. Add B and C in two repositories. Verify the first event matches either exact URL and hold its terminal notice through the polling interval. No search may start while the other event is unsent. Return the first event's accepted `delivery_notice`, expect the other exact URL once, and return its accepted notice. Return an accepted notice promptly for each subsequent event. Repeat unchanged, complete an observation without A, then reintroduce A and expect its URL once. After acceptance, serve incomplete and rate-limited results omitting A, honor Retry-After, then recover with A/B/C plus new D. Expect only D and safe coverage and failure diagnostics. The fixture substitutes GitHub query evaluation. The delivery walkthrough exercises excluded PRs on GitHub. |
| GRR-V03-RESTART | AC-005, AC-007, NFR-002 | Use the actual hub/client, instrumented plugin command and model output with controlled GitHub. With A present, establish a silent baseline for the first registration, add B and confirm its acceptance, then register a second destination and establish its silent baseline with A+B. Stop normally and verify A/B persisted with the correct statuses per registration. Restart both retained routes with A/B/C, holding the later watch validation as below. Each saved destination receives only C after activation. Stop and replace one payload schema with an incompatible value. Startup must reject that restore without falling back to a fresh observation or delivering output. Explicitly reset only that watch in the stopped tree, then restart with A/B/C plus new D. The reset watch stays silent and persists the current set as baseline; the other watch receives only D. |

Keep the full failure/scheduling/schema matrix in unit tests instead of
repeating it through processes. Combine silent startup, direct matching, and
partial-retrieval recovery in V02. Use the existing held-response gates and
wait for the next search request before sampling a checkpoint or asserting
silence for the preceding result. For the emission gate, hold the first event's notice
after its event and select on the next-request channel for 125 ms at the
40 ms test interval. A request during that window fails immediately. Check
the exact interval boundary with a controlled clock in U-SCHEDULE. V03 tests
registration-specific progress
through real storage. AC-009 and AC-010 use the same plugin state and
observation functions in unit tests because the hub saves opaque envelopes
and has no schema-dependent path. Additional process restarts for these
payload variants would repeat the storage behavior exercised by V03. Reuse common
[TRS-V01/V02/V03](trigger-runtime-state.md#verification-strategy) for reset,
association, core save failure and restoration rules; V03 adds the actual
GitHub state adapter and identity binding. U-STATE verifies plugin-specific
save-independent polling and invalid progress. Do not duplicate the common
filesystem fault harness or add a real-service CI matrix. Common checks alone
do not prove this plugin's restart behavior.

For V01/V02 use the existing public go-plugin launch/dispense/Session recipe,
with opaque caller IDs, rather than a hub or a second external integration.
For V03 use existing daemon/client lifecycle and model-output artifact patterns.
Inspect saved IDs in filename restoration order before restart. Reset the HTTPS identity-request counter for restart; sequential filename-order
restoration identifies the second `/user` request to hold, independent of random
UUID creation order. Use distinct valid watch intervals to keep both saved
registrations distinct without needing different credentials or hosts.
Wait for the first watch's positive-result journal barrier with A/B/C
already available. The HTTPS server must record zero search calls across both
watches during a bounded 500 ms observation window after the later validation
request arrives. Select on the first-search request channel and a
context deadline: any search before release fails immediately; this is an
explicit negative observation, not a sleep used to schedule restoration. Release
within the existing eight-second validation budget. After release, expect
A/B/C in each first search and exactly one accepted C per saved destination.
Use the first-complete diagnostic and final checkpoint to verify creation,
restoration, and reset of each baseline. Before each stop, correlate new
model-output rows with the hub's accepted `delivery_result` diagnostic by
registration and delivery ID. Record public grant/result ordering in
the instrumented plugin journal and actual output observations. Use channels
and request-journal barriers, not random ID creation order or fixed sleeps.
This catches start-after-result even when search beats restoration. Common
TRS-V04 covers later rejection/no grants; do not duplicate its subprocess matrix.
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
The common runtime-state v2 probe demonstrates watch-result/checkpoint/grant
ordering and dormant/quiesced handling over the same transport. Holding an HTTP
handler with channels and request counters uses the proven HTTPS boundary; no
product test flag, dependency, shared harness or CI change is needed. Product
implementation must still pass the deterministic actual plugin/hub regression.

Budget: build each fixture/test command once per suite, with at most 30 seconds
for warm-dependency build/setup, V01/V02 at most 15 seconds each, V03 at most
45 seconds including the additional reset restart, and final cleanup at most
five seconds. The total is 110 seconds,
leaving ten seconds within the existing 120-second scoped suite deadline;
at most one TLS fixture and, for V03, one daemon plus configured plugin processes
per phase. A cold dependency download is separate setup evidence. Reuse the
existing Go integration job and its dependencies; no CI, dependency-version,
shared harness, timeout, or verification-judgment changes are required. Record
actual probe costs and any unavailable environment in the PR, never as product
acceptance. The proposed case budget requires design review agreement.

The existing Ubuntu Go CI uses the toolchain from `go.mod` and runs the
unit and tagged product integration targets separately. Run from the
repository root:

```sh
go run build/mage.go -d build -w . test
go run build/mage.go -d build -w . integrationAll
go test -tags=integration ./plugins/triggers/github-review-request -v
go run build/mage.go -d build -w . guardrails
```

The generic integration target discovers the suite automatically. Compile and
enumerate all three cases during design, including the two static Pending cases.
Implementation requires a passing result for every inventory ID.

The scoped inventory is
`plugins/triggers/github-review-request/testdata/github-review-request-cases.json`.
Also run
the existing common runtime-state inventory separately for reused TRS coverage;
neither inventory replaces the other.

### Delivery quality checks

| Check | Mapping | Preconditions, actions and expected result | Evidence and evaluator |
| --- | --- | --- | --- |
| GRR-D01 | AC-001/002/003/005/008 | Run the shipping plugin on github.com with controlled reviewers, requester, team and two repositories. Perform the baseline and restart walkthrough and the authentication matrix below. Resume the retained watch again. Introduce a direct request for the watched reviewer in the other repository, a team-only request, a direct request addressed only to a different reviewer, and closed and merged PRs. Expect only the new direct open URL. Cancel the watched reviewer's direct request on A while leaving A open. Wait for a successful periodic checkpoint without A, then re-request review from the watched reviewer on A and expect its URL once. | The delivery maintainer records the shipping revision, redacted configuration, host, credential type and permissions, stimuli, URL events, acceptance outcomes, diagnostics and successful saves and exits. |
| GRR-D02 | AC-001/002/003/005/008, Q-001 | Repeat D01 on a qualified GHES 3.19.x installation with a known patch version and controlled repositories, users and team. Verify host isolation as described below. | The delivery maintainer records the exact patch and API profile, installation owner, redacted destination metadata and a separate result with the D01 evidence. |

For the baseline and restart walkthrough, use an isolated reviewer account
with controlled requests so unrelated matches cannot interfere. Choose a
poll interval long enough to collect the first observation's checkpoint
before a second poll. Use the issue IDs of the controlled PRs to correlate
results with saved rows.

1. Directly request review from the controlled reviewer on open PR A before
   registering. Wait for the registration's first-complete diagnostic with
   `baseline_created=true` and one match. While the watch runs, use its
   successful periodic save record to identify a checkpoint with A at status 3.
   Verify there is no A output before requesting B.
2. Keep A present and directly request review from the same reviewer on open
   PR B. Wait for its exact URL and confirmed action acceptance. Stop normally
   and verify the final checkpoint has A at status 3
   and B at status 2. Record the successful save and process exits.
3. Keep A and B present and restart the retained watch. Wait for the same
   registration's first-complete diagnostic with two matches and
   `baseline_created=false`. Stop and verify a successful final checkpoint
   before the next poll. Verify the checkpoint has A at status 3 and B at
   status 2, with no additional URL events. Retain the diagnostic, final save,
   exit and output records together to assess silence after an actual observation.

Inspect saved progress read-only using the core runtime-state envelope. Record
only the controlled row assertions and their correlation to the observation
and save. Keep tokens and unrelated checkpoint contents out of retained evidence.

For authentication on each host, use a fresh watch for each condition and
compare the authenticated user ID in a successful checkpoint with the
expected test identity. Use two valid environment tokens for distinct test
users and omit `gh` from PATH. Verify that setting both selects the
`GH_TOKEN` identity and setting only `GITHUB_TOKEN` selects its identity.
Then use a rejected `GH_TOKEN` with that same valid `GITHUB_TOKEN` and
expect a rejected registration and authentication diagnostic. With both
variables empty, verify acceptance using the saved login for the selected
host and rejection in an environment with no usable saved login.

For host isolation, use separate daemon environments, state roots and
control addresses for the github.com and GHES tokens. Record the HTTPS
destination host and port for each plugin instance, using a TLS pass-through
CONNECT log or equivalent per-instance egress metadata. Verify that the
github.com instance connects only to `api.github.com:443` and the GHES
instance only to its configured API host on port 443. Correlate those
destinations with the expected authenticated identities and completed
observations. Retain only destination metadata and the identity assertions.

Real-service execution may be deferred, but remains visibly unverified until
these checks run. Review finalized README/examples for all FR-010 topics,
normal recovery, exact capacity and coverage warnings, and secret exclusions.
GRR-D01 and GRR-D02 require independent live evidence. The
[service-free model-plugin walkthrough and end-to-end suite](../requirements/bundled-plugins.md#scope)
verify the shared routing path separately.

## Change impact and open decisions

The plugin uses the service-neutral `runtime_state_v2` coordination defined by
[the runtime design](trigger-runtime-state.md). Upgrade by stopping normally and
preserving the complete state tree before switching binaries. Compatible v1
progress is read in memory and converted to v2 on the next successful save.
Older plugin binaries reject a saved v2 payload. For rollback, stop every
owner and restore the preserved compatible state tree, which resumes from
that earlier save, or explicitly reset the affected progress. A reset under
the older binary uses its initial-notification behavior. See the
[offline reset procedure](trigger-runtime-state.md#offline-reset-deletion-and-recreation).

| Question or risk | Impact | Owner | Resolve before | Status |
| --- | --- | --- | --- | --- |
| Q-001: GHES environment and patch version | A usable installation is needed for the GRR-D02 walkthrough | Responsible human and delivery maintainer | Design approval for the environment assignment; delivery for execution | API target, permissions and qualification criteria specified. Assign an installation, patch version, owner and authorized access before approval. |
| Q-002: Search domain and capacity | At most 1,000 matches within GitHub-selected 4,000-repository coverage. Exceeding repository coverage cannot reliably be detected. | Design reviewer | Design approval | The bounded search, exact-ID encoding, and per-watch API cost are subject to design approval. |
| Eventual indexing and non-atomic pagination | Brief changes and some concurrent churn are not observable | Operator | Usage | Accepted observation-model limit; detect reported/observable incompleteness and preserve state. |
| Instrumented CI versus live services | Controlled TLS/process success cannot certify deployed GitHub behavior | Delivery maintainer | Delivery completion | D01/D02 remain required and separate. |

Unassigned GHES environment planning prevents an unconditional design-readiness
claim. Deferring live execution does not remove that decision or waive required
delivery acceptance.
