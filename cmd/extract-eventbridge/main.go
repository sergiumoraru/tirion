package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

type schedule struct {
	RuleName           string `json:"ruleName"`
	ScheduleExpression string `json:"scheduleExpression"`
	TargetType         string `json:"targetType"`
	TargetName         string `json:"targetName"`
	State              string `json:"state"`
	Source             string `json:"source"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	defaultDB := os.Getenv("DATABASE_URL")
	dbURL := flag.String("db", "", "PostgreSQL connection string (required; defaults to DATABASE_URL)")
	filePath := flag.String("file", "", "JSON array of schedule records; see SETUP.md#importing-external-schedules")
	verbose := flag.Bool("v", false, "Verbose output")
	dryRun := flag.Bool("dry-run", false, "Validate and display records without database access")
	flag.Parse()
	if flag.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "extract-eventbridge: unexpected argument(s) %q; this command takes only flags\n", flag.Args())
		os.Exit(2)
	}
	if *dbURL == "" {
		*dbURL = defaultDB
	}
	if strings.TrimSpace(*filePath) == "" {
		return fmt.Errorf("-file is required")
	}
	f, err := os.Open(*filePath)
	if err != nil {
		return err
	}
	defer f.Close()
	schedules, err := readSchedules(f)
	if err != nil {
		return fmt.Errorf("read schedules: %w", err)
	}
	if *dryRun {
		for _, s := range schedules {
			fmt.Printf("[%s] %s %s -> %s (%s) %s\n",
				s.Source, s.RuleName, s.ScheduleExpression, s.TargetName, s.TargetType, s.State)
		}
		fmt.Printf("Dry run: %d valid schedules; no data changed.\n", len(schedules))
		return nil
	}

	ctx := context.Background()
	if *dbURL == "" {
		log.Fatal("database is required: set DATABASE_URL or pass -db; .env files are not loaded automatically (see SETUP.md#configuration-reference)")
	}

	pool, err := pgxpool.New(ctx, *dbURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	// Upsert supplied identities only; a partial export must not erase unrelated schedules.
	for _, s := range schedules {
		if _, err := tx.Exec(ctx, `INSERT INTO eventbridge_schedules
			(rule_name, schedule_expression, target_type, target_name, state, source)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (rule_name, target_name) DO UPDATE SET
				schedule_expression = EXCLUDED.schedule_expression,
				target_type = EXCLUDED.target_type,
				state = EXCLUDED.state,
				source = EXCLUDED.source`,
			s.RuleName, s.ScheduleExpression, s.TargetType, s.TargetName, s.State, s.Source); err != nil {
			return fmt.Errorf("store schedule %q: %w", s.RuleName, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if *verbose {
		for _, s := range schedules {
			fmt.Printf("Imported %s -> %s\n", s.RuleName, s.TargetName)
		}
	}
	fmt.Printf("Imported %d schedules. Unspecified schedules were preserved.\n", len(schedules))
	return nil
}

func readSchedules(r io.Reader) ([]schedule, error) {
	decoder := json.NewDecoder(r)
	decoder.DisallowUnknownFields()
	var records []schedule
	if err := decoder.Decode(&records); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("expected one JSON array")
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("schedule array must not be empty")
	}
	seen := make(map[[2]string]bool)
	for i := range records {
		s := &records[i]
		s.RuleName = strings.TrimSpace(s.RuleName)
		s.ScheduleExpression = strings.TrimSpace(s.ScheduleExpression)
		s.TargetName = strings.TrimSpace(s.TargetName)
		s.TargetType = strings.ToLower(strings.TrimSpace(s.TargetType))
		s.State = strings.ToUpper(strings.TrimSpace(s.State))
		s.Source = strings.ToLower(strings.TrimSpace(s.Source))
		if s.RuleName == "" || s.ScheduleExpression == "" || s.TargetName == "" || s.TargetType == "" {
			return nil, fmt.Errorf("record %d requires ruleName, scheduleExpression, targetName and targetType", i+1)
		}
		if s.State != "ENABLED" && s.State != "DISABLED" {
			return nil, fmt.Errorf("record %d: state must be ENABLED or DISABLED", i+1)
		}
		if s.Source != "eventbridge" && s.Source != "scheduler" {
			return nil, fmt.Errorf("record %d: source must be eventbridge or scheduler", i+1)
		}
		key := [2]string{s.RuleName, s.TargetName}
		if seen[key] {
			return nil, fmt.Errorf("record %d: duplicate rule/target identity", i+1)
		}
		seen[key] = true
	}
	return records, nil
}
