//go:build integration

package main

import . "github.com/onsi/ginkgo/v2"

// Governed by docs/design/trigger-runtime-state.md. These static Pending cases
// describe implementation acceptance; their registration is not acceptance.
var _ = Describe("Trigger runtime state command acceptance", Serial, func() {
	It("restores confirmed progress in isolation and starts recreated registrations fresh",
		Label("case:TRS-V01-RESTART-ISOLATION"), Pending, func() {
			// Inputs: real daemon/client/store and public-protocol participating
			// verification input, with two registrations R1/R2, distinct argument
			// keys, distinct accepted cursors A1/B1, and observable output A/B.
			// The input owns no state files or storage service. Its restored
			// probe-v1 payload records accepted update IDs; stopped-period input
			// is supplied explicitly after startup and is not hub replay.
			// Action: register both and repeat an equivalent active registration.
			// Immediately after a completed periodic checkpoint, publish A1/B1,
			// observe accepted output and the input's matching delivery notices,
			// then issue normal stop before another periodic checkpoint. The
			// fixture journal must show these final accepted updates enter the
			// quiesce checkpoint, with no intervening periodic snapshot. Await
			// daemon/configured-child exits. Restart the same config/state and
			// wait for readiness without re-registration. Present A1/B1 again,
			// then new A2/B2. Stop; preserve a copy and use the documented offline
			// deletion procedure for R1 and its runtime state. Restart and
			// recreate R1 with exactly its old arguments, then present A1 again.
			// Expect: duplicate registration keeps R1's ID and progress; restored
			// final stop checkpoint saves the just-accepted A1/B1 progress;
			// watches receive only their owning schema/payload before starting;
			// A1/B1 are not notified again after the first restart, A2/B2 are
			// each notified once to their original destinations. Recreated R1
			// has a fresh ID and null initial state and can notify A1 once;
			// retained R2 keeps B progress. State never selects a recipient.
			// Boundary: confirmed acceptance and successful normal-stop save,
			// not recipient processing, crash recovery or general deduplication.
		})

	It("blocks invalid state before watch startup and permits only deliberate reset",
		Label("case:TRS-V02-RESTORE-RECOVERY"), Pending, func() {
			// Inputs: retained participating registration with successfully saved
			// probe-v1 progress, real daemon/store and journalled verification
			// input; an untouched saved-state copy for each independent variant.
			// Action: while stopped, separately corrupt the newest generation
			// and supply a valid envelope with an incompatible trigger schema.
			// Start each variant; observe diagnostics and absence of affected
			// watch/event/delivery. Preserve the state, follow the documented
			// explicit offline reset, and restart with the original registration.
			// Expect: affected trigger/registration and bounded reason are visible,
			// payload contents are absent, no older generation or initial state
			// is silently substituted, and the affected watch never starts on
			// failure. Deliberate reset retains the registration ID/destination,
			// returns null state and permits the fixture's documented initial
			// behavior. Preserve files on failed restoration for operator repair.
			// Read/application error matrices and denied-read injection are unit
			// checks; assembled variants cover actual persisted bytes and RPC
			// compatibility rejection without relying on host permission tricks.
		})

	It("reports failed saving while monitoring and fresh notifications continue",
		Label("case:TRS-V03-SAVE-FAILURE"), Pending, func() {
			// Inputs: active participating registration, real daemon/store,
			// controlled public input and observable action; one saved cursor.
			// Action: after readiness and an initial successful checkpoint,
			// obstruct only that registration's runtime save location with a
			// regular file (fixture fault injection, not supported live editing).
			// Advance accepted progress, wait for the scheduled save failure,
			// then publish a fresh U2 while input/action remain available.
			// Expect: diagnostic identifies trigger/registration, operation and
			// reason, explicitly warns next-restart continuity is not guaranteed,
			// and does not report successful persistence. Input stays alive;
			// U2 is accepted once through the unchanged action contract and no
			// automatic retry occurs. Last good committed generation is retained;
			// failed staging never becomes a successful generation. Restore the
			// obstruction during cleanup and await normal stop/child exits.
			// Failed-save data carries no continuity claim. Unit checks cover
			// individual write/close/rename and cleanup failures and payload bounds.
		})
})
