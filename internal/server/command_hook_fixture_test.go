package server

// Command-hook contract tests exercise a real shell when installed. Allow
// process startup on a contended/race CI runner; these cases assert output,
// rewriting and blocking, not a five-second shell-startup performance target.
// Explicit timeout/cancellation tests keep their own short deadlines.
const commandHookFixtureTimeoutSeconds = 30
