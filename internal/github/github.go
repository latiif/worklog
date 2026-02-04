package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"standup-report/internal/report"
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

func FetchEvents(ctx context.Context, token string, since, until time.Time) ([]report.Event, error) {
	username, err := getUser(ctx, token)
	if err != nil {
		return nil, fmt.Errorf("getting user: %w", err)
	}

	var events []report.Event
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
				return events, nil
			}
			if e.CreatedAt.After(until) {
				continue
			}
			events = append(events, parseEvent(e)...)
		}
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
