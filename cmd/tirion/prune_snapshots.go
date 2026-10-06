package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/sergiumoraru/tirion/internal/graph"
	"log"
	"os"
	"time"
)

func runPruneSnapshots(args []string) {
	fs := flag.NewFlagSet("prune-snapshots", flag.ExitOnError)
	fallback := os.Getenv("DATABASE_URL")
	db := databaseFlag(fs, fallback)
	age := fs.Duration("older-than", 30*24*time.Hour, "Minimum generation age (at least 24h)")
	keep := fs.Int("keep", 2, "Successful generations to retain per workspace/repository (at least 1)")
	apply := fs.Bool("apply", false, "Delete eligible generations; without this flag, only list candidates")
	_ = fs.Parse(args)
	if fs.NArg() != 0 || *age < 24*time.Hour || *keep < 1 {
		log.Fatal("use -older-than >=24h, -keep >=1, and no positional arguments")
	}
	storage, err := graph.NewStorage(*db)
	if err != nil {
		log.Fatal(err)
	}
	defer storage.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	snapshots, err := storage.PruneSnapshots(ctx, *age, *keep, *apply)
	if err != nil {
		log.Fatal(err)
	}
	for _, s := range snapshots {
		fmt.Printf("%d\t%s\t%s\t%s\n", s.ID, s.Workspace, s.Repo, s.CreatedAt.Format(time.RFC3339))
	}
	action := "Eligible (dry run)"
	if *apply {
		action = "Deleted"
	}
	fmt.Printf("%s: %d snapshots\n", action, len(snapshots))
}
