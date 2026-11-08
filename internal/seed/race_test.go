//go:build race

package seed_test

// raceDetector is true when the tests run under -race, which slows seeding
// by an order of magnitude; the boot-time budget is then not meaningful.
const raceDetector = true
