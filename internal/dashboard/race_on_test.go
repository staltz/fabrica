//go:build race

package dashboard

// raceEnabled says the tests run under the race detector, which makes
// everything several times slower, so timing checks stand down.
const raceEnabled = true
