package report

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"
)

var categoryOrder = []EventCategory{
	CategoryPR,
	CategoryReview,
	CategoryReviewComment,
	CategoryIssue,
	CategoryComment,
	CategoryCommit,
	CategoryPipeline,
	CategoryPendingReview,
}

func Generate(events []Event, since, until time.Time, format string) string {
	switch format {
	case "table":
		return generateTable(events, since, until)
	case "json":
		return generateJSON(events, since, until)
	default:
		return generateText(events, since, until)
	}
}

func generateText(events []Event, since, until time.Time) string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("Standup Report (%s – %s)\n",
		since.Format("Jan 2"), until.Format("Jan 2")))
	b.WriteString(strings.Repeat("=", 40) + "\n\n")

	grouped := groupByCategory(events)

	for _, cat := range categoryOrder {
		catEvents := grouped[cat]
		if len(catEvents) == 0 {
			continue
		}

		header := string(cat)
		if cat == CategoryPendingReview {
			header += " (current)"
		}
		b.WriteString(fmt.Sprintf("%s:\n", header))
		for _, e := range catEvents {
			action := capitalize(e.Action)
			b.WriteString(fmt.Sprintf("  - %s %s [%s] (%s)\n",
				action, e.Title, e.Source, e.Repo))
		}
		b.WriteString("\n")
	}

	if len(events) == 0 {
		b.WriteString("No activity found for this period.\n")
	}

	return b.String()
}

func generateTable(events []Event, _, _ time.Time) string {
	var b strings.Builder

	w := tabwriter.NewWriter(&b, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "CATEGORY\tACTION\tTITLE\tSOURCE\tREPO\tDATE")

	grouped := groupByCategory(events)

	for _, cat := range categoryOrder {
		catEvents := grouped[cat]
		for _, e := range catEvents {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
				string(cat), capitalize(e.Action), e.Title, e.Source, e.Repo,
				e.CreatedAt.Format("2006-01-02"))
		}
	}

	w.Flush()
	return b.String()
}

func generateJSON(events []Event, since, until time.Time) string {
	type jsonEvent struct {
		Category  string `json:"category"`
		Action    string `json:"action"`
		Title     string `json:"title"`
		URL       string `json:"url"`
		Repo      string `json:"repo"`
		Source    string `json:"source"`
		CreatedAt string `json:"created_at"`
	}

	type jsonReport struct {
		Since  string      `json:"since"`
		Until  string      `json:"until"`
		Events []jsonEvent `json:"events"`
	}

	sorted := sortedEvents(events)

	je := make([]jsonEvent, len(sorted))
	for i, e := range sorted {
		je[i] = jsonEvent{
			Category:  string(e.Category),
			Action:    e.Action,
			Title:     e.Title,
			URL:       e.URL,
			Repo:      e.Repo,
			Source:    e.Source,
			CreatedAt: e.CreatedAt.Format(time.RFC3339),
		}
	}

	r := jsonReport{
		Since:  since.Format("2006-01-02"),
		Until:  until.Format("2006-01-02"),
		Events: je,
	}

	data, _ := json.MarshalIndent(r, "", "  ")
	return string(data) + "\n"
}

// groupByCategory groups events by category and sorts each group newest-first.
func groupByCategory(events []Event) map[EventCategory][]Event {
	grouped := make(map[EventCategory][]Event)
	for _, e := range events {
		grouped[e.Category] = append(grouped[e.Category], e)
	}
	for _, catEvents := range grouped {
		sort.Slice(catEvents, func(i, j int) bool {
			return catEvents[i].CreatedAt.After(catEvents[j].CreatedAt)
		})
	}
	return grouped
}

// sortedEvents returns events sorted by category order, then newest-first within each category.
func sortedEvents(events []Event) []Event {
	catIndex := make(map[EventCategory]int)
	for i, cat := range categoryOrder {
		catIndex[cat] = i
	}

	sorted := make([]Event, len(events))
	copy(sorted, events)

	sort.SliceStable(sorted, func(i, j int) bool {
		ci, cj := catIndex[sorted[i].Category], catIndex[sorted[j].Category]
		if ci != cj {
			return ci < cj
		}
		return sorted[i].CreatedAt.After(sorted[j].CreatedAt)
	})

	return sorted
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	runes := []rune(s)
	runes[0] = unicode.ToUpper(runes[0])
	return string(runes)
}
