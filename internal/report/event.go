package report

import "time"

type EventCategory string

const (
	CategoryCommit        EventCategory = "Commits"
	CategoryPR            EventCategory = "Pull Requests / Merge Requests"
	CategoryReview        EventCategory = "Code Reviews"
	CategoryReviewComment EventCategory = "Review Comments"
	CategoryIssue         EventCategory = "Issues"
	CategoryComment       EventCategory = "Comments"
	CategoryPipeline      EventCategory = "CI Pipeline Failures"
	CategoryPendingReview EventCategory = "Pending Reviews"
)

type Event struct {
	Category  EventCategory
	Action    string
	Title     string
	URL       string
	Repo      string
	Source    string // "github" or "gitlab"
	CreatedAt time.Time
}
