package cmd

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"standup-report/internal/github"
	"standup-report/internal/gitlab"
	"standup-report/internal/report"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"
	naturaldate "github.com/tj/go-naturaldate"
)

var (
	sinceFlag string
	untilFlag string
)

var rootCmd = &cobra.Command{
	Use:   "standup-report",
	Short: "Generate a standup report from GitHub and GitLab activity",
	RunE:  run,
}

func init() {
	rootCmd.Flags().StringVar(&sinceFlag, "since", "", `start date inclusive, e.g. "2026-01-28", "yesterday", "2 weeks ago" (default: 7 days ago)`)
	rootCmd.Flags().StringVar(&untilFlag, "until", "", `end date inclusive, e.g. "2026-02-04", "today", "last friday" (default: today)`)
}

func Execute() error {
	return rootCmd.Execute()
}

func run(cmd *cobra.Command, args []string) error {
	// Load .env file without overriding existing env vars.
	// Precedence: real env vars > .env file values.
	_ = godotenv.Load()

	since, until, err := parseDateRange(sinceFlag, untilFlag)
	if err != nil {
		return err
	}

	githubToken := os.Getenv("GITHUB_TOKEN")
	gitlabToken := os.Getenv("GITLAB_TOKEN")

	if githubToken == "" && gitlabToken == "" {
		return fmt.Errorf("at least one of GITHUB_TOKEN or GITLAB_TOKEN must be set")
	}

	ctx := context.Background()
	var allEvents []report.Event
	var mu sync.Mutex
	var wg sync.WaitGroup
	var errs []error

	if githubToken != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			events, err := github.FetchEvents(ctx, githubToken, since, until)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Errorf("github: %w", err))
				return
			}
			allEvents = append(allEvents, events...)
		}()
	}

	if gitlabToken != "" {
		wg.Add(1)
		go func() {
			defer wg.Done()
			events, err := gitlab.FetchEvents(ctx, gitlabToken, since, until)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Errorf("gitlab: %w", err))
				return
			}
			allEvents = append(allEvents, events...)
		}()
	}

	wg.Wait()

	for _, err := range errs {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
	}

	output := report.Generate(allEvents, since, until)
	fmt.Print(output)
	return nil
}

const dateFormat = "2006-01-02"

// parseDateRange resolves the --since and --until flag values into a [since, until] time range.
//
// Both flags accept either an exact date (YYYY-MM-DD) or a natural language expression
// such as "yesterday", "2 weeks ago", or "last monday". Exact dates are tried first;
// if parsing fails, the input is interpreted as natural language relative to the current time.
//
// Both boundaries are inclusive:
//   - --since is normalized to the start of the resolved day (00:00:00).
//   - --until is normalized to the end of the resolved day (23:59:59).
//
// Defaults when omitted: --since = 7 days ago, --until = today.
func parseDateRange(sinceStr, untilStr string) (time.Time, time.Time, error) {
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	var since time.Time
	if sinceStr == "" {
		since = today.AddDate(0, 0, -7)
	} else {
		t, err := parseDate(sinceStr, now)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid --since value %q: %w", sinceStr, err)
		}
		since = startOfDay(t)
	}

	var until time.Time
	if untilStr == "" {
		until = endOfDay(today)
	} else {
		t, err := parseDate(untilStr, now)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid --until value %q: %w", untilStr, err)
		}
		until = endOfDay(t)
	}

	if since.After(until) {
		return time.Time{}, time.Time{}, fmt.Errorf("--since (%s) must be before --until (%s)",
			since.Format(dateFormat), until.Format(dateFormat))
	}

	return since, until, nil
}

// parseDate tries YYYY-MM-DD first, then falls back to natural language parsing
// via go-naturaldate. The ref time is used as the reference point for relative
// expressions (e.g. "2 weeks ago" is relative to ref).
func parseDate(s string, ref time.Time) (time.Time, error) {
	if t, err := time.ParseInLocation(dateFormat, s, ref.Location()); err == nil {
		return t, nil
	}
	return naturaldate.Parse(s, ref)
}

func startOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

func endOfDay(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 23, 59, 59, 0, t.Location())
}
