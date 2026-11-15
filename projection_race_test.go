//go:build race

package catalog_test

// Absolute latency is checked by the mandatory uninstrumented performance run.
// Race builds still verify the same data, counts, and relative regression bound.
const projectionRaceBuild = true
