package github

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	gh "github.com/google/go-github/v69/github"
	"worklog/internal/report"
)

func FetchEvents(ctx context.Context, token string, since, until time.Time) ([]report.Event, error) {
	client := gh.NewClient(nil).WithAuthToken(token)

	u, _, err := client.Users.Get(ctx, "")
	if err != nil {
		return nil, fmt.Errorf("getting user: %w", err)
	}
	username := u.GetLogin()

	var events []report.Event
	repos := make(map[string]struct{})
	// seenSHAs tracks commit SHAs from the event stream so that the commit
	// search (phase 2) can skip duplicates.
	seenSHAs := make(map[string]struct{})

	opts := &gh.ListOptions{PerPage: 100}
	for page := 1; page <= 10; page++ {
		opts.Page = page
		ghEvents, _, err := client.Activity.ListEventsPerformedByUser(ctx, username, false, opts)
		if err != nil {
			return nil, err
		}
		if len(ghEvents) == 0 {
			break
		}
		done := false
		for _, e := range ghEvents {
			createdAt := e.GetCreatedAt().Time
			if createdAt.Before(since) {
				done = true
				break
			}
			if createdAt.After(until) {
				continue
			}
			repoName := e.GetRepo().GetName()
			repos[repoName] = struct{}{}
			if e.GetType() == "PushEvent" {
				if p, err := e.ParsePayload(); err == nil {
					if push, ok := p.(*gh.PushEvent); ok {
						for _, c := range push.Commits {
							seenSHAs[c.GetSHA()] = struct{}{}
						}
					}
				}
			}
			events = append(events, parseEvent(e)...)
		}
		if done {
			break
		}
	}

	var mu sync.Mutex
	var wg sync.WaitGroup

	wg.Add(3)
	go func() {
		defer wg.Done()
		ciEvents, err := fetchCIFailures(ctx, client, username, repos, since, until)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: github CI failures: %v\n", err)
			return
		}
		mu.Lock()
		events = append(events, ciEvents...)
		mu.Unlock()
	}()

	go func() {
		defer wg.Done()
		prEvents, err := fetchPendingReviews(ctx, client, username)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: github pending reviews: %v\n", err)
			return
		}
		mu.Lock()
		events = append(events, prEvents...)
		mu.Unlock()
	}()

	go func() {
		defer wg.Done()
		commitEvents, err := fetchCommits(ctx, client, username, since, until, seenSHAs)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: github commit search: %v\n", err)
			return
		}
		mu.Lock()
		events = append(events, commitEvents...)
		mu.Unlock()
	}()

	wg.Wait()
	return events, nil
}

func fetchCIFailures(ctx context.Context, client *gh.Client, username string, repos map[string]struct{}, since, until time.Time) ([]report.Event, error) {
	var events []report.Event
	for repoName := range repos {
		parts := strings.SplitN(repoName, "/", 2)
		if len(parts) != 2 {
			continue
		}
		owner, repo := parts[0], parts[1]
		opts := &gh.ListWorkflowRunsOptions{
			Actor:       username,
			Status:      "failure",
			Created:     ">=" + since.Format("2006-01-02"),
			ListOptions: gh.ListOptions{PerPage: 100},
		}
		result, _, err := client.Actions.ListRepositoryWorkflowRuns(ctx, owner, repo, opts)
		if err != nil {
			continue
		}
		for _, run := range result.WorkflowRuns {
			createdAt := run.GetCreatedAt().Time
			if createdAt.After(until) {
				continue
			}
			events = append(events, report.Event{
				Category:  report.CategoryPipeline,
				Action:    "failed",
				Title:     fmt.Sprintf("%s on %s", run.GetName(), run.GetHeadBranch()),
				URL:       run.GetHTMLURL(),
				Repo:      repoName,
				Source:    "github",
				CreatedAt: createdAt,
			})
		}
	}
	return events, nil
}

func fetchPendingReviews(ctx context.Context, client *gh.Client, username string) ([]report.Event, error) {
	query := fmt.Sprintf("is:pr is:open review-requested:%s", username)
	opts := &gh.SearchOptions{ListOptions: gh.ListOptions{PerPage: 100}}
	result, _, err := client.Search.Issues(ctx, query, opts)
	if err != nil {
		return nil, err
	}

	var events []report.Event
	for _, item := range result.Issues {
		repoName := ""
		if parts := strings.SplitN(item.GetRepositoryURL(), "/repos/", 2); len(parts) == 2 {
			repoName = parts[1]
		}
		events = append(events, report.Event{
			Category:  report.CategoryPendingReview,
			Action:    "awaiting your review",
			Title:     fmt.Sprintf("#%d %s", item.GetNumber(), item.GetTitle()),
			URL:       item.GetHTMLURL(),
			Repo:      repoName,
			Source:    "github",
			CreatedAt: item.GetCreatedAt().Time,
		})
	}
	return events, nil
}

// fetchCommits uses the commit search API to find commits the user authored
// in all accessible repos (including private ones). It skips SHAs already
// seen in the event stream to avoid duplicates.
func fetchCommits(ctx context.Context, client *gh.Client, username string, since, until time.Time, seenSHAs map[string]struct{}) ([]report.Event, error) {
	query := fmt.Sprintf("author:%s author-date:%s..%s", username,
		since.Format("2006-01-02"), until.Format("2006-01-02"))

	var events []report.Event
	opts := &gh.SearchOptions{ListOptions: gh.ListOptions{PerPage: 100}}
	for {
		results, resp, err := client.Search.Commits(ctx, query, opts)
		if err != nil {
			return nil, err
		}
		for _, c := range results.Commits {
			if _, seen := seenSHAs[c.GetSHA()]; seen {
				continue
			}
			authorDate := c.GetCommit().GetAuthor().GetDate().Time
			events = append(events, report.Event{
				Category:  report.CategoryCommit,
				Action:    "pushed",
				Title:     firstLine(c.GetCommit().GetMessage()),
				URL:       c.GetHTMLURL(),
				Repo:      c.GetRepository().GetFullName(),
				Source:    "github",
				CreatedAt: authorDate,
			})
		}
		if resp.NextPage == 0 {
			break
		}
		opts.Page = resp.NextPage
	}
	return events, nil
}

func parseEvent(e *gh.Event) []report.Event {
	payload, err := e.ParsePayload()
	if err != nil {
		return nil
	}
	repoName := e.GetRepo().GetName()
	createdAt := e.GetCreatedAt().Time

	switch p := payload.(type) {
	case *gh.PushEvent:
		if len(p.Commits) == 0 {
			branch := strings.TrimPrefix(p.GetRef(), "refs/heads/")
			return []report.Event{{
				Category:  report.CategoryCommit,
				Action:    "pushed",
				Title:     fmt.Sprintf("to %s", branch),
				Repo:      repoName,
				Source:    "github",
				CreatedAt: createdAt,
			}}
		}
		var evts []report.Event
		for _, c := range p.Commits {
			evts = append(evts, report.Event{
				Category:  report.CategoryCommit,
				Action:    "pushed",
				Title:     firstLine(c.GetMessage()),
				Repo:      repoName,
				Source:    "github",
				CreatedAt: createdAt,
			})
		}
		return evts

	case *gh.PullRequestEvent:
		action := p.GetAction()
		if action == "closed" && p.GetPullRequest().GetMerged() {
			action = "merged"
		}
		return []report.Event{{
			Category:  report.CategoryPR,
			Action:    action,
			Title:     fmt.Sprintf("#%d %s", p.GetPullRequest().GetNumber(), p.GetPullRequest().GetTitle()),
			URL:       p.GetPullRequest().GetHTMLURL(),
			Repo:      repoName,
			Source:    "github",
			CreatedAt: createdAt,
		}}

	case *gh.PullRequestReviewEvent:
		return []report.Event{{
			Category:  report.CategoryReview,
			Action:    p.GetReview().GetState(),
			Title:     fmt.Sprintf("#%d %s", p.GetPullRequest().GetNumber(), p.GetPullRequest().GetTitle()),
			URL:       p.GetReview().GetHTMLURL(),
			Repo:      repoName,
			Source:    "github",
			CreatedAt: createdAt,
		}}

	case *gh.PullRequestReviewCommentEvent:
		return []report.Event{{
			Category:  report.CategoryReviewComment,
			Action:    "commented",
			Title:     fmt.Sprintf("#%d %s", p.GetPullRequest().GetNumber(), p.GetPullRequest().GetTitle()),
			URL:       p.GetComment().GetHTMLURL(),
			Repo:      repoName,
			Source:    "github",
			CreatedAt: createdAt,
		}}

	case *gh.IssuesEvent:
		return []report.Event{{
			Category:  report.CategoryIssue,
			Action:    p.GetAction(),
			Title:     fmt.Sprintf("#%d %s", p.GetIssue().GetNumber(), p.GetIssue().GetTitle()),
			URL:       p.GetIssue().GetHTMLURL(),
			Repo:      repoName,
			Source:    "github",
			CreatedAt: createdAt,
		}}

	case *gh.IssueCommentEvent:
		return []report.Event{{
			Category:  report.CategoryComment,
			Action:    "commented",
			Title:     fmt.Sprintf("#%d %s", p.GetIssue().GetNumber(), p.GetIssue().GetTitle()),
			URL:       p.GetComment().GetHTMLURL(),
			Repo:      repoName,
			Source:    "github",
			CreatedAt: createdAt,
		}}
	}
	return nil
}

func firstLine(s string) string {
	for i := range s {
		if s[i] == '\n' {
			return s[:i]
		}
	}
	return s
}
