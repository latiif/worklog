package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"standup-report/internal/report"
)

type user struct {
	ID       int    `json:"id"`
	Username string `json:"username"`
}

type event struct {
	ID          int       `json:"id"`
	ActionName  string    `json:"action_name"`
	TargetType  string    `json:"target_type"`
	TargetTitle string    `json:"target_title"`
	TargetIID   int       `json:"target_iid"`
	CreatedAt   time.Time `json:"created_at"`
	PushData    *pushData `json:"push_data"`
	Note        *note     `json:"note"`
	ProjectID   int       `json:"project_id"`
}

type pushData struct {
	CommitCount int    `json:"commit_count"`
	CommitTitle string `json:"commit_title"`
	Ref         string `json:"ref"`
}

type note struct {
	Body         string `json:"body"`
	NoteableType string `json:"noteable_type"`
}

type project struct {
	PathWithNamespace string `json:"path_with_namespace"`
	WebURL            string `json:"web_url"`
}

func FetchEvents(ctx context.Context, token string, since, until time.Time) ([]report.Event, error) {
	baseURL := baseURL()

	u, err := getUser(ctx, baseURL, token)
	if err != nil {
		return nil, fmt.Errorf("getting user: %w", err)
	}

	projectCache := make(map[int]*project)
	var events []report.Event

	for page := 1; page <= 100; page++ {
		endpoint := fmt.Sprintf("%s/api/v4/users/%d/events?per_page=100&page=%d&after=%s&before=%s",
			baseURL, u.ID, page, since.Format("2006-01-02"), until.AddDate(0, 0, 1).Format("2006-01-02"))

		req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("PRIVATE-TOKEN", token)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}

		var glEvents []event
		if err := json.NewDecoder(resp.Body).Decode(&glEvents); err != nil {
			resp.Body.Close()
			return nil, err
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
		}

		if len(glEvents) == 0 {
			break
		}

		for _, e := range glEvents {
			if e.CreatedAt.Before(since) {
				continue
			}

			proj, err := resolveProject(ctx, baseURL, token, e.ProjectID, projectCache)
			if err != nil {
				continue
			}

			events = append(events, parseEvent(e, proj)...)
		}
	}

	return events, nil
}

func baseURL() string {
	if u := os.Getenv("GITLAB_URL"); u != "" {
		return strings.TrimRight(u, "/")
	}
	return "https://gitlab.com"
}

func getUser(ctx context.Context, baseURL, token string) (*user, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", baseURL+"/api/v4/user", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	var u user
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return nil, err
	}
	return &u, nil
}

func resolveProject(ctx context.Context, baseURL, token string, projectID int, cache map[int]*project) (*project, error) {
	if p, ok := cache[projectID]; ok {
		return p, nil
	}

	endpoint := fmt.Sprintf("%s/api/v4/projects/%d", baseURL, projectID)
	req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("PRIVATE-TOKEN", token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	var proj project
	if err := json.NewDecoder(resp.Body).Decode(&proj); err != nil {
		return nil, err
	}
	cache[projectID] = &proj
	return &proj, nil
}

func parseEvent(e event, proj *project) []report.Event {
	repoName := proj.PathWithNamespace

	switch {
	case e.PushData != nil:
		title := e.PushData.CommitTitle
		if title == "" {
			title = fmt.Sprintf("%d commit(s) to %s", e.PushData.CommitCount, e.PushData.Ref)
		}
		return []report.Event{{
			Category:  report.CategoryCommit,
			Action:    "pushed",
			Title:     title,
			Repo:      repoName,
			Source:    "gitlab",
			CreatedAt: e.CreatedAt,
		}}

	case e.TargetType == "MergeRequest":
		cat := report.CategoryPR
		if e.ActionName == "approved" {
			cat = report.CategoryReview
		}
		return []report.Event{{
			Category:  cat,
			Action:    e.ActionName,
			Title:     fmt.Sprintf("!%d %s", e.TargetIID, e.TargetTitle),
			URL:       fmt.Sprintf("%s/-/merge_requests/%d", proj.WebURL, e.TargetIID),
			Repo:      repoName,
			Source:    "gitlab",
			CreatedAt: e.CreatedAt,
		}}

	case e.TargetType == "Issue":
		return []report.Event{{
			Category:  report.CategoryIssue,
			Action:    e.ActionName,
			Title:     fmt.Sprintf("#%d %s", e.TargetIID, e.TargetTitle),
			URL:       fmt.Sprintf("%s/-/issues/%d", proj.WebURL, e.TargetIID),
			Repo:      repoName,
			Source:    "gitlab",
			CreatedAt: e.CreatedAt,
		}}

	case e.Note != nil:
		return []report.Event{{
			Category:  report.CategoryComment,
			Action:    "commented",
			Title:     e.TargetTitle,
			Repo:      repoName,
			Source:    "gitlab",
			CreatedAt: e.CreatedAt,
		}}
	}

	return nil
}
