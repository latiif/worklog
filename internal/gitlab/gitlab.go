package gitlab

import (
	"context"
	"fmt"
	"maps"
	"os"
	"strings"
	"sync"
	"time"

	gl "gitlab.com/gitlab-org/api/client-go"
	"worklog/internal/report"
)

func FetchEvents(ctx context.Context, token string, since, until time.Time) ([]report.Event, error) {
	client, err := newClient(token)
	if err != nil {
		return nil, err
	}

	u, _, err := client.Users.CurrentUser(gl.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("getting user: %w", err)
	}

	projectCache := make(map[int64]*gl.Project)
	projectIDs := make(map[int64]struct{})
	var events []report.Event

	afterTime := gl.ISOTime(since)
	beforeTime := gl.ISOTime(until.AddDate(0, 0, 1))

	for page := int64(1); page <= 100; page++ {
		opts := &gl.ListContributionEventsOptions{
			After:       &afterTime,
			Before:      &beforeTime,
			ListOptions: gl.ListOptions{PerPage: 100, Page: page},
		}
		glEvents, resp, err := client.Events.ListCurrentUserContributionEvents(opts, gl.WithContext(ctx))
		if err != nil {
			return nil, err
		}
		if len(glEvents) == 0 {
			break
		}
		for _, e := range glEvents {
			proj, err := resolveProject(ctx, client, e.ProjectID, projectCache)
			if err != nil {
				continue
			}
			projectIDs[e.ProjectID] = struct{}{}
			events = append(events, parseEvent(e, proj)...)
		}
		if resp.NextPage == 0 {
			break
		}
	}

	// Phase 2: Fetch CI failures and pending reviews in parallel.
	// Take a snapshot of the project cache for read-only use by goroutines.
	cacheSnapshot := make(map[int64]*gl.Project, len(projectCache))
	maps.Copy(cacheSnapshot, projectCache)

	var mu sync.Mutex
	var wg sync.WaitGroup

	wg.Add(2)
	go func() {
		defer wg.Done()
		ciEvents, err := fetchCIFailures(ctx, client, u.Username, projectIDs, cacheSnapshot, since, until)
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
		prEvents, err := fetchPendingReviews(ctx, client, u.ID, cacheSnapshot)
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

func fetchCIFailures(ctx context.Context, client *gl.Client, username string, projectIDs map[int64]struct{}, cache map[int64]*gl.Project, since, until time.Time) ([]report.Event, error) {
	var events []report.Event
	status := gl.Failed
	for pid := range projectIDs {
		proj := cache[pid]
		if proj == nil {
			continue
		}
		opts := &gl.ListProjectPipelinesOptions{
			Status:        &status,
			Username:      new(username),
			UpdatedAfter:  new(since),
			UpdatedBefore: new(until.AddDate(0, 0, 1)),
			ListOptions:   gl.ListOptions{PerPage: 100},
		}
		pipelines, _, err := client.Pipelines.ListProjectPipelines(pid, opts, gl.WithContext(ctx))
		if err != nil {
			continue
		}
		for _, p := range pipelines {
			updatedAt := time.Time{}
			if p.UpdatedAt != nil {
				updatedAt = *p.UpdatedAt
			}
			events = append(events, report.Event{
				Category:  report.CategoryPipeline,
				Action:    "failed",
				Title:     fmt.Sprintf("pipeline #%d on %s", p.ID, p.Ref),
				URL:       p.WebURL,
				Repo:      proj.PathWithNamespace,
				Source:    "gitlab",
				CreatedAt: updatedAt,
			})
		}
	}
	return events, nil
}

func fetchPendingReviews(ctx context.Context, client *gl.Client, userID int64, cacheSnapshot map[int64]*gl.Project) ([]report.Event, error) {
	opts := &gl.ListMergeRequestsOptions{
		State:       new("opened"),
		ReviewerID:  gl.ReviewerID(userID),
		Scope:       new("all"),
		ListOptions: gl.ListOptions{PerPage: 100},
	}
	mrs, _, err := client.MergeRequests.ListMergeRequests(opts, gl.WithContext(ctx))
	if err != nil {
		return nil, err
	}

	// Local cache for project lookups (avoids races with the shared cache).
	localCache := make(map[int64]*gl.Project)
	maps.Copy(localCache, cacheSnapshot)

	var events []report.Event
	for _, mr := range mrs {
		proj, err := resolveProject(ctx, client, mr.ProjectID, localCache)
		if err != nil {
			continue
		}
		createdAt := time.Time{}
		if mr.CreatedAt != nil {
			createdAt = *mr.CreatedAt
		}
		events = append(events, report.Event{
			Category:  report.CategoryPendingReview,
			Action:    "awaiting your review",
			Title:     fmt.Sprintf("!%d %s", mr.IID, mr.Title),
			URL:       mr.WebURL,
			Repo:      proj.PathWithNamespace,
			Source:    "gitlab",
			CreatedAt: createdAt,
		})
	}
	return events, nil
}

func newClient(token string) (*gl.Client, error) {
	opts := []gl.ClientOptionFunc{}
	if u := os.Getenv("GITLAB_URL"); u != "" {
		opts = append(opts, gl.WithBaseURL(strings.TrimRight(u, "/")))
	}
	return gl.NewClient(token, opts...)
}

func resolveProject(ctx context.Context, client *gl.Client, projectID int64, cache map[int64]*gl.Project) (*gl.Project, error) {
	if p, ok := cache[projectID]; ok {
		return p, nil
	}
	proj, _, err := client.Projects.GetProject(projectID, &gl.GetProjectOptions{}, gl.WithContext(ctx))
	if err != nil {
		return nil, err
	}
	cache[projectID] = proj
	return proj, nil
}

func parseEvent(e *gl.ContributionEvent, proj *gl.Project) []report.Event {
	repoName := proj.PathWithNamespace
	createdAt := time.Time{}
	if e.CreatedAt != nil {
		createdAt = *e.CreatedAt
	}

	switch {
	case e.PushData.Ref != "":
		if e.PushData.CommitCount == 0 && e.PushData.CommitTitle == "" {
			return nil
		}
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
			CreatedAt: createdAt,
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
			CreatedAt: createdAt,
		}}

	case e.TargetType == "Issue":
		return []report.Event{{
			Category:  report.CategoryIssue,
			Action:    e.ActionName,
			Title:     fmt.Sprintf("#%d %s", e.TargetIID, e.TargetTitle),
			URL:       fmt.Sprintf("%s/-/issues/%d", proj.WebURL, e.TargetIID),
			Repo:      repoName,
			Source:    "gitlab",
			CreatedAt: createdAt,
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
			CreatedAt: createdAt,
		}}
	}

	return nil
}
