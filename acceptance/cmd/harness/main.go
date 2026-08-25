// Command harness runs the in-repo acceptance/benchmark experiments against a
// deployed local Google Maps scraper container.
//
// It drives ONE job at a time through the container's local HTTP API: it never
// contacts Google Maps itself. Point it at a container the operator controls
// with -base, choose an experiment (or the whole catalog), and it records a
// durable JSON and human summary per run under -out.
//
// The command performs real scrapes when pointed at a real container. Run it
// only against a container and a target you intend to scrape.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/gosom/google-maps-scraper/acceptance"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

type options struct {
	base             string
	token            string
	experiment       string
	out              string
	repeat           int
	poll             time.Duration
	timeout          time.Duration
	noResources      bool
	list             bool
	dryRun           bool
	queries          string
	gridBBox         string
	gridCellKM       float64
	runtimeSeconds   int
	marketConc       int
	escalationLadder string
}

func run(args []string, stdout io.Writer) error {
	opts, flagSet, err := parseFlags(args)
	if err != nil {
		return err
	}

	catalogOptions, err := buildCatalogOptions(opts)
	if err != nil {
		return err
	}

	experiments, err := resolveExperiments(opts.experiment, catalogOptions)
	if err != nil {
		flagSet.Usage()

		return err
	}

	if opts.list {
		return printList(stdout, experiments)
	}

	if opts.dryRun {
		return printDryRun(stdout, experiments)
	}

	if strings.TrimSpace(opts.base) == "" {
		flagSet.Usage()

		return errors.New("-base is required to run experiments (use -list or -dry-run to inspect configs offline)")
	}

	client, err := acceptance.NewClient(opts.base, acceptance.WithToken(opts.token))
	if err != nil {
		return err
	}

	store, err := acceptance.NewStore(opts.out)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	runOptions := acceptance.RunOptions{
		PollInterval:    opts.poll,
		MaxWait:         opts.timeout,
		SampleResources: !opts.noResources,
	}

	return execute(ctx, stdout, client, store, experiments, runOptions)
}

func parseFlags(args []string) (options, *flag.FlagSet, error) {
	var opts options
	flagSet := flag.NewFlagSet("harness", flag.ContinueOnError)
	flagSet.StringVar(&opts.base, "base", "", "base URL of the deployed container, e.g. http://127.0.0.1:8099")
	flagSet.StringVar(&opts.token, "token", "", "optional API bearer token (only if the container enabled API keys or login)")
	flagSet.StringVar(&opts.experiment, "experiment", "escalation", "experiment id (A..E, fast, sparse, medium, dense) or group (escalation, markets, all)")
	flagSet.StringVar(&opts.out, "out", "acceptance-results", "output directory for records")
	flagSet.IntVar(&opts.repeat, "repeat", 1, "runs per experiment (>=2 produces a repeatability/variance report)")
	flagSet.DurationVar(&opts.poll, "poll", 0, "progress poll interval (default 5s)")
	flagSet.DurationVar(&opts.timeout, "timeout", 0, "max wait for a terminal state (default runtime limit + 5m)")
	flagSet.BoolVar(&opts.noResources, "no-resources", false, "do not sample app-reported system metrics while polling")
	flagSet.BoolVar(&opts.list, "list", false, "print the resolved experiment ids and labels, then exit")
	flagSet.BoolVar(&opts.dryRun, "dry-run", false, "print the resolved job requests as JSON without creating any job")
	flagSet.StringVar(&opts.queries, "queries", "", "override workload queries (newline or '||' separated)")
	flagSet.StringVar(&opts.gridBBox, "grid-bbox", "", "override grid bounding box minLat,minLon,maxLat,maxLon")
	flagSet.Float64Var(&opts.gridCellKM, "grid-cell-km", 0, "override grid cell size in km")
	flagSet.IntVar(&opts.runtimeSeconds, "runtime", 0, "override per-job runtime limit in seconds (default 3600)")
	flagSet.IntVar(&opts.marketConc, "market-concurrency", 0, "browser concurrency for the market experiments (default 1)")
	flagSet.StringVar(&opts.escalationLadder, "escalation", "", "override A..E concurrency ladder, e.g. A=1,B=2,C=4,D=8,E=16")

	if err := flagSet.Parse(args); err != nil {
		return options{}, flagSet, err
	}

	return opts, flagSet, nil
}

func buildCatalogOptions(opts options) (acceptance.CatalogOptions, error) {
	catalogOptions := acceptance.DefaultCatalogOptions()
	catalogOptions.Repeat = opts.repeat
	if opts.marketConc > 0 {
		catalogOptions.MarketConcurrency = opts.marketConc
	}

	if queries := parseQueries(opts.queries); len(queries) > 0 {
		catalogOptions.Workload.Queries = queries
	}
	if strings.TrimSpace(opts.gridBBox) != "" {
		catalogOptions.Workload.GridBBox = strings.TrimSpace(opts.gridBBox)
	}
	if opts.gridCellKM > 0 {
		catalogOptions.Workload.GridCellKM = opts.gridCellKM
	}
	if opts.runtimeSeconds > 0 {
		catalogOptions.Workload.RuntimeSeconds = int64(opts.runtimeSeconds)
	}

	if strings.TrimSpace(opts.escalationLadder) != "" {
		ladder, err := parseLadder(opts.escalationLadder)
		if err != nil {
			return acceptance.CatalogOptions{}, err
		}
		catalogOptions.Escalation = ladder
	}

	return catalogOptions, nil
}

func parseQueries(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	fields := strings.FieldsFunc(raw, func(r rune) bool {
		return r == '\n'
	})
	if len(fields) <= 1 {
		fields = strings.Split(raw, "||")
	}
	queries := make([]string, 0, len(fields))
	for _, field := range fields {
		if value := strings.TrimSpace(field); value != "" {
			queries = append(queries, value)
		}
	}

	return queries
}

func parseLadder(raw string) (map[string]int, error) {
	ladder := map[string]int{}
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		key, value, found := strings.Cut(pair, "=")
		if !found {
			return nil, fmt.Errorf("invalid escalation entry %q: want ID=concurrency", pair)
		}
		concurrency, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("invalid escalation concurrency in %q: %w", pair, err)
		}
		ladder[strings.ToUpper(strings.TrimSpace(key))] = concurrency
	}
	if len(ladder) == 0 {
		return nil, errors.New("escalation ladder is empty")
	}

	return ladder, nil
}

func resolveExperiments(selector string, catalogOptions acceptance.CatalogOptions) ([]acceptance.ExperimentConfig, error) {
	switch strings.ToLower(strings.TrimSpace(selector)) {
	case "", "escalation":
		return acceptance.Escalation(catalogOptions), nil
	case "markets":
		return acceptance.Markets(catalogOptions), nil
	case "all", "catalog":
		return acceptance.Catalog(catalogOptions), nil
	}

	config, ok := acceptance.Experiment(selector, catalogOptions)
	if !ok {
		return nil, fmt.Errorf("unknown experiment %q (want A..E, fast, sparse, medium, dense, escalation, markets, or all)", selector)
	}

	return []acceptance.ExperimentConfig{config}, nil
}

func printList(stdout io.Writer, experiments []acceptance.ExperimentConfig) error {
	for _, config := range experiments {
		fmt.Fprintf(stdout, "%-8s %s\n", config.ID, config.Label)
	}

	return nil
}

func printDryRun(stdout io.Writer, experiments []acceptance.ExperimentConfig) error {
	for _, config := range experiments {
		payload, err := json.MarshalIndent(config.Job, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintf(stdout, "# experiment %s — %s\n%s\n\n", config.ID, config.Label, payload)
	}

	return nil
}

func execute(
	ctx context.Context,
	stdout io.Writer,
	client *acceptance.Client,
	store *acceptance.Store,
	experiments []acceptance.ExperimentConfig,
	runOptions acceptance.RunOptions,
) error {
	for _, config := range experiments {
		fmt.Fprintf(stdout, "== experiment %s (%s) x%d ==\n", config.ID, config.Label, maxInt(config.Repeat, 1))

		records, report, runErr := acceptance.RunRepeated(ctx, client, config, runOptions)
		for _, record := range records {
			paths, err := store.Save(record)
			if err != nil {
				return err
			}
			fmt.Fprint(stdout, acceptance.FormatSummary(record))
			fmt.Fprintf(stdout, "  saved         : %s\n\n", paths.JSON)
		}

		if len(records) > 1 {
			paths, err := store.SaveRepeatability(report)
			if err != nil {
				return err
			}
			fmt.Fprint(stdout, acceptance.FormatRepeatability(report))
			fmt.Fprintf(stdout, "  saved         : %s\n\n", paths.JSON)
		}

		if runErr != nil {
			return fmt.Errorf("experiment %s: %w", config.ID, runErr)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}

	return nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}

	return b
}
