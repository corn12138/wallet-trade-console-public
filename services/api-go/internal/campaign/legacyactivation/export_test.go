package legacyactivation

// SetActivationHookForTest runs hook inside the activation transaction after
// the comparison passed, while the campaign's fence lock is held.
func SetActivationHookForTest(hook func()) func() {
	previous := activationHook
	activationHook = hook
	return func() { activationHook = previous }
}

// SetReviewHookForTest runs hook inside the review transaction after the
// replay lookup found no decision for the command, before anything is written.
func SetReviewHookForTest(hook func()) func() {
	previous := reviewHook
	reviewHook = hook
	return func() { reviewHook = previous }
}
