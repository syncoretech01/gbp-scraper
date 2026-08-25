// Package acceptance is a bounded, in-repo real-world acceptance and benchmark
// harness for the local Google Maps scraper web application.
//
// It drives ONE scrape job at a time through the existing local HTTP API of a
// deployed container: it creates the job (POST /api/v1/jobs), polls the job
// progress until the run reaches a terminal state, and then reads the durable
// readback endpoints (coverage, benchmark, logs, worker events, results, and
// app-reported system metrics). From those it computes a stable, comparable
// ExperimentRecord: wall time, discovered rows, unique businesses, rows per
// minute, task success rate, browser-failure rate, block rate, retry count,
// duplicate rate, effective concurrency, and the checkpoint/recovery outcome.
//
// The harness never contacts Google Maps itself. It only speaks to the local
// application's API; the application performs any scraping. Its unit tests run
// entirely against an in-process fake HTTP server that returns canned JSON, so
// the test suite proves the harness parses and records every metric correctly
// without touching the network or the live workspace.
//
// The lead points the harness at a real deployed container for the actual
// live runs. Two runs of the same experiment configuration always produce the
// same record shape, so two code versions can be diffed field by field, and a
// repeated experiment additionally reports the variance of its headline
// metrics.
package acceptance
