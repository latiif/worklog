package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"worklog/internal/report"
)

type event struct {
	Type      string          `json:"type"`
	Repo      repo            `json:"repo"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"created_at"`
}

type repo struct {
	Name string `json:"name"`
}

type user struct {
	Login string `json:"login"`
}

type pushPayload struct {
	Size    int      `json:"size"`
	Commits []commit `json:"commits"`
}

type commit struct {
	SHA     string `json:"sha"`
	Message string `json:"message"`
}

type prPayload struct {
	Action      string      `json:"action"`
	PullRequest pullRequest `json:"pull_request"`
}

type pullRequest struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	HTMLURL string `json:"html_url"`
	Merged  bool   `json:"merged"`
}

type prReviewPayload struct {
	Action      string      `json:"action"`
	Review      review      `json:"review"`
	PullRequest pullRequest `json:"pull_request"`
}

type review struct {
	State   string `json:"state"`
	HTMLURL string `json:"html_url"`
}

type prReviewCommentPayload struct {
	Action      string        `json:"action"`
	Comment     reviewComment `json:"comment"`
	PullRequest pullRequest   `json:"pull_request"`
}

type reviewComment struct {
	HTMLURL string `json:"html_url"`
}

type issuesPayload struct {
	Action string `json:"action"`
	Issue  issue  `json:"issue"`
}

type issue struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	HTMLURL string `json:"html_url"`
}

type issueCommentPayload struct {
	Action  string  `json:"action"`
	Issue   issue   `json:"issue"`
	Comment comment `json:"comment"`
}

type comment struct {
	HTMLURL string `json:"html_url"`
}

type workflowRun struct {
	Name       string    `json:"name"`
	HeadBranch string    `json:"head_branch"`
	HTMLURL    string    `json:"html_url"`
	CreatedAt  time.Time `json:"created_at"`
}

type workflowRunsResponse struct {
	WorkflowRuns []workflowRun `json:"workflow_runs"`
}

type searchIssuesResponse struct {
	Items []searchIssue `json:"items"`
}

type searchIssue struct {
	Number        int    `json:"number"`
	Title         string `json:"title"`
	HTMLURL       string `json:"html_url"`
	RepositoryURL string `json:"repository_url"`
	CreatedAt     time.Time `json:"created_at"`
}

func FetchEvents(ctx context.Context, token string, since, until time.Time) ([]report.Event, error) {
	username, err := getUser(ctx, token)
	if err != nil {
		return nil, fmt.Errorf("getting user: %w", err)
	}

	// Phase 1: Fetch user events and collect repo names.
	var events []report.Event
	repos := make(map[string]struct{})

	for page := 1; page <= 10; page++ {
		url := fmt.Sprintf("https://api.github.com/users/%s/events?per_page=100&page=%d", username, page)
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/vnd.github+json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}

		var ghEvents []event
		if err := json.NewDecoder(resp.Body).Decode(&ghEvents); err != nil {
			resp.Body.Close()
			return nil, err
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
		}

		if len(ghEvents) == 0 {
			break
		}

		for _, e := range ghEvents {
			if e.CreatedAt.Before(since) {
				goto phase2
			}
			if e.CreatedAt.After(until) {
				continue
			}
			repos[e.Repo.Name] = struct{}{}
			events = append(events, parseEvent(e)...)
		}
	}

phase2:
	// Phase 2: Fetch CI failures and pending reviews in parallel.
	var mu sync.Mutex
	var wg sync.WaitGroup

	wg.Add(2)
	go func() {
		defer wg.Done()
		ciEvents, err := fetchCIFailures(ctx, token, username, repos, since, until)
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
		prEvents, err := fetchPendingReviews(ctx, token, username)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: github pending reviews: %v\n", err)
			return
		}
		mu.Lock()
		events = append(events, prEvents...)
		mu.Unlock()
	}()

	wg.Wait()

	return events, nil
}

func fetchCIFailures(ctx context.Context, token, username string, repos map[string]struct{}, since, until time.Time) ([]report.Event, error) {
	var events []report.Event
	for repoName := range repos {
		url := fmt.Sprintf("https://api.github.com/repos/%s/actions/runs?actor=%s&status=failure&created=%%3E%s&per_page=100",
			repoName, username, since.Format("2006-01-02"))
		req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			continue
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/vnd.github+json")

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			continue
		}

		var result workflowRunsResponse
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			resp.Body.Close()
			continue
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			continue
		}

		for _, run := range result.WorkflowRuns {
			if run.CreatedAt.After(until) {
				continue
			}
			events = append(events, report.Event{
				Category:  report.CategoryPipeline,
				Action:    "failed",
				Title:     fmt.Sprintf("%s on %s", run.Name, run.HeadBranch),
				URL:       run.HTMLURL,
				Repo:      repoName,
				Source:    "github",
				CreatedAt: run.CreatedAt,
			})
		}
	}
	return events, nil
}

func fetchPendingReviews(ctx context.Context, token, username string) ([]report.Event, error) {
	url := fmt.Sprintf("https://api.github.com/search/issues?q=is:pr+is:open+review-requested:%s&per_page=100", username)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	var result searchIssuesResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}

	var events []report.Event
	for _, item := range result.Items {
		// Extract repo name from repository_url: "https://api.github.com/repos/owner/repo"
		repoName := ""
		if parts := strings.SplitN(item.RepositoryURL, "/repos/", 2); len(parts) == 2 {
			repoName = parts[1]
		}
		events = append(events, report.Event{
			Category:  report.CategoryPendingReview,
			Action:    "awaiting your review",
			Title:     fmt.Sprintf("#%d %s", item.Number, item.Title),
			URL:       item.HTMLURL,
			Repo:      repoName,
			Source:    "github",
			CreatedAt: item.CreatedAt,
		})
	}
	return events, nil
}

func getUser(ctx context.Context, token string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/user", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	var u user
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return "", err
	}
	return u.Login, nil
}

func parseEvent(e event) []report.Event {
	switch e.Type {
	case "PushEvent":
		var p pushPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return nil
		}
		var events []report.Event
		for _, c := range p.Commits {
			msg := firstLine(c.Message)
			events = append(events, report.Event{
				Category:  report.CategoryCommit,
				Action:    "pushed",
				Title:     msg,
				Repo:      e.Repo.Name,
				Source:    "github",
				CreatedAt: e.CreatedAt,
			})
		}
		return events

	case "PullRequestEvent":
		var p prPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return nil
		}
		action := p.Action
		if action == "closed" && p.PullRequest.Merged {
			action = "merged"
		}
		return []report.Event{{
			Category:  report.CategoryPR,
			Action:    action,
			Title:     fmt.Sprintf("#%d %s", p.PullRequest.Number, p.PullRequest.Title),
			URL:       p.PullRequest.HTMLURL,
			Repo:      e.Repo.Name,
			Source:    "github",
			CreatedAt: e.CreatedAt,
		}}

	case "PullRequestReviewEvent":
		var p prReviewPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return nil
		}
		return []report.Event{{
			Category:  report.CategoryReview,
			Action:    p.Review.State,
			Title:     fmt.Sprintf("#%d %s", p.PullRequest.Number, p.PullRequest.Title),
			URL:       p.Review.HTMLURL,
			Repo:      e.Repo.Name,
			Source:    "github",
			CreatedAt: e.CreatedAt,
		}}

	case "PullRequestReviewCommentEvent":
		var p prReviewCommentPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return nil
		}
		return []report.Event{{
			Category:  report.CategoryReviewComment,
			Action:    "commented",
			Title:     fmt.Sprintf("#%d %s", p.PullRequest.Number, p.PullRequest.Title),
			URL:       p.Comment.HTMLURL,
			Repo:      e.Repo.Name,
			Source:    "github",
			CreatedAt: e.CreatedAt,
		}}

	case "IssuesEvent":
		var p issuesPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return nil
		}
		return []report.Event{{
			Category:  report.CategoryIssue,
			Action:    p.Action,
			Title:     fmt.Sprintf("#%d %s", p.Issue.Number, p.Issue.Title),
			URL:       p.Issue.HTMLURL,
			Repo:      e.Repo.Name,
			Source:    "github",
			CreatedAt: e.CreatedAt,
		}}

	case "IssueCommentEvent":
		var p issueCommentPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return nil
		}
		return []report.Event{{
			Category:  report.CategoryComment,
			Action:    "commented",
			Title:     fmt.Sprintf("#%d %s", p.Issue.Number, p.Issue.Title),
			URL:       p.Comment.HTMLURL,
			Repo:      e.Repo.Name,
			Source:    "github",
			CreatedAt: e.CreatedAt,
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
