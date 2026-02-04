package gitlab

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
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

type pipeline struct {
	ID        int       `json:"id"`
	Status    string    `json:"status"`
	Ref       string    `json:"ref"`
	WebURL    string    `json:"web_url"`
	UpdatedAt time.Time `json:"updated_at"`
	User      struct {
		ID int `json:"id"`
	} `json:"user"`
}

type mergeRequest struct {
	IID       int    `json:"iid"`
	Title     string `json:"title"`
	WebURL    string `json:"web_url"`
	ProjectID int    `json:"project_id"`
	CreatedAt time.Time `json:"created_at"`
}

func FetchEvents(ctx context.Context, token string, since, until time.Time) ([]report.Event, error) {
	baseURL := baseURL()

	u, err := getUser(ctx, baseURL, token)
	if err != nil {
		return nil, fmt.Errorf("getting user: %w", err)
	}

	// Phase 1: Fetch user events, build project cache, collect project IDs.
	projectCache := make(map[int]*project)
	projectIDs := make(map[int]struct{})
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

			projectIDs[e.ProjectID] = struct{}{}
			events = append(events, parseEvent(e, proj)...)
		}
	}

	// Phase 2: Fetch CI failures and pending reviews in parallel.
	// Take a snapshot of the project cache for read-only use by goroutines.
	cacheSnapshot := make(map[int]*project, len(projectCache))
	for k, v := range projectCache {
		cacheSnapshot[k] = v
	}

	var mu sync.Mutex
	var wg sync.WaitGroup

	wg.Add(2)
	go func() {
		defer wg.Done()
		ciEvents, err := fetchCIFailures(ctx, baseURL, token, u.ID, projectIDs, cacheSnapshot, since, until)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: gitlab CI failures: %v\n", err)
			return
		}
		mu.Lock()
		events = append(events, ciEvents...)
		mu.Unlock()
	}()

	go func() {
		defer wg.Done()
		prEvents, err := fetchPendingReviews(ctx, baseURL, token, u.ID, cacheSnapshot)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: gitlab pending reviews: %v\n", err)
			return
		}
		mu.Lock()
		events = append(events, prEvents...)
		mu.Unlock()
	}()

	wg.Wait()

	return events, nil
}

func fetchCIFailures(ctx context.Context, baseURL, token string, userID int, projectIDs map[int]struct{}, cache map[int]*project, since, until time.Time) ([]report.Event, error) {
	var events []report.Event
	for pid := range projectIDs {
		proj := cache[pid]
		if proj == nil {
			continue
		}

		endpoint := fmt.Sprintf("%s/api/v4/projects/%d/pipelines?status=failed&updated_after=%s&updated_before=%s&per_page=100",
			baseURL, pid, since.Format("2006-01-02"), until.AddDate(0, 0, 1).Format("2006-01-02"))

		req, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
		if err != nil {
			continue
		}
		req.Header.Set("PRIVATE-TOKEN", token)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			continue
		}

		var pipelines []pipeline
		if err := json.NewDecoder(resp.Body).Decode(&pipelines); err != nil {
			resp.Body.Close()
			continue
		}
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			continue
		}

		for _, p := range pipelines {
			if p.User.ID != userID {
				continue
			}
			events = append(events, report.Event{
				Category:  report.CategoryPipeline,
				Action:    "failed",
				Title:     fmt.Sprintf("pipeline #%d on %s", p.ID, p.Ref),
				URL:       p.WebURL,
				Repo:      proj.PathWithNamespace,
				Source:    "gitlab",
				CreatedAt: p.UpdatedAt,
			})
		}
	}
	return events, nil
}

func fetchPendingReviews(ctx context.Context, baseURL, token string, userID int, cacheSnapshot map[int]*project) ([]report.Event, error) {
	endpoint := fmt.Sprintf("%s/api/v4/merge_requests?state=opened&reviewer_id=%d&scope=all&per_page=100",
		baseURL, userID)

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

	var mrs []mergeRequest
	if err := json.NewDecoder(resp.Body).Decode(&mrs); err != nil {
		return nil, err
	}

	// Local cache for project lookups (avoids races with the shared cache).
	localCache := make(map[int]*project)
	for k, v := range cacheSnapshot {
		localCache[k] = v
	}

	var events []report.Event
	for _, mr := range mrs {
		proj, err := resolveProject(ctx, baseURL, token, mr.ProjectID, localCache)
		if err != nil {
			continue
		}
		events = append(events, report.Event{
			Category:  report.CategoryPendingReview,
			Action:    "awaiting your review",
			Title:     fmt.Sprintf("!%d %s", mr.IID, mr.Title),
			URL:       mr.WebURL,
			Repo:      proj.PathWithNamespace,
			Source:    "gitlab",
			CreatedAt: mr.CreatedAt,
		})
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
		cat := report.CategoryComment
		if e.Note.NoteableType == "MergeRequest" {
			cat = report.CategoryReviewComment
		}
		return []report.Event{{
			Category:  cat,
			Action:    "commented",
			Title:     e.TargetTitle,
			Repo:      repoName,
			Source:    "gitlab",
			CreatedAt: e.CreatedAt,
		}}
	}

	return nil
}
