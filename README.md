# worklog
[![Go Report Card](https://goreportcard.com/badge/github.com/latiif/worklog)](https://goreportcard.com/report/github.com/latiif/worklog)
![GitHub last commit](https://img.shields.io/github/last-commit/latiif/worklog)

A single command that turns your GitHub and GitLab activity into a ready-to-post standup report. No more tab-switching or trying to remember what you did yesterday — `worklog` pulls your PRs, reviews, commits, issues, comments, CI failures, and pending review requests into one view.

## Install

```
go install ./...
```

## Quick start

```bash
# Set at least one token (see "Creating tokens" below)
export GITHUB_TOKEN="github_pat_..."
export GITLAB_TOKEN="glpat-..."

# Default: last 7 days
worklog

# Yesterday only
worklog --since yesterday --until yesterday

# Custom range
worklog --since "2 weeks ago" --until "last friday"

# Table or JSON output
worklog -o table
worklog -o json
```

Tokens can also be placed in a `.env` file in the working directory. Real environment variables take precedence over `.env` values.

## Flags

| Flag | Short | Default | Description |
|------|-------|---------|-------------|
| `--since` | | 7 days ago | Start date (inclusive). Accepts `YYYY-MM-DD` or natural language like `"yesterday"`, `"2 weeks ago"`. |
| `--until` | | today | End date (inclusive). Same formats as `--since`. |
| `--output` | `-o` | `text` | Output format: `text`, `table`, or `json`. |

## What it reports

- **Pull Requests / Merge Requests** — opened, merged, closed
- **Code Reviews** — approvals, changes requested
- **Review Comments** — inline review comments
- **Issues** — opened, closed
- **Comments** — issue and discussion comments
- **Commits** — pushes
- **CI Pipeline Failures** — your failed workflow runs / pipelines
- **Pending Reviews** — open PRs/MRs currently awaiting your review

## Creating tokens

### GitHub Personal Access Token

1. Go to **Settings > Developer settings > Personal access tokens > Fine-grained tokens** ([direct link](https://github.com/settings/personal-access-tokens/new)).
2. Give the token a descriptive name (e.g. `worklog`).
3. Set an expiration.
4. Under **Repository access**, select **All repositories** (or limit to specific repos you want tracked).
5. Under **Permissions > Repository permissions**, grant:
   - **Actions** — Read-only (needed for CI failure reporting)
   - **Issues** — Read-only
   - **Pull requests** — Read-only
   - **Metadata** — Read-only (automatically selected)
6. Click **Generate token** and copy the value.
7. Export it: `export GITHUB_TOKEN="github_pat_..."` or add `GITHUB_TOKEN=github_pat_...` to your `.env` file.

### GitLab Personal Access Token

1. Go to **Preferences > Access Tokens** ([direct link for gitlab.com](https://gitlab.com/-/user_settings/personal_access_tokens)).
2. Give the token a descriptive name (e.g. `worklog`).
3. Set an expiration date.
4. Select the following **scopes**:
   - **read_api** — read access to events, merge requests, pipelines, and projects
5. Click **Create personal access token** and copy the value.
6. Export it: `export GITLAB_TOKEN="glpat-..."` or add `GITLAB_TOKEN=glpat-...` to your `.env` file.

For self-hosted GitLab, also set `GITLAB_URL`:

```
export GITLAB_URL="https://gitlab.yourcompany.com"
```

## Environment variables

| Variable | Required | Description |
|----------|----------|-------------|
| `GITHUB_TOKEN` | At least one token required | GitHub personal access token |
| `GITLAB_TOKEN` | At least one token required | GitLab personal access token |
| `GITLAB_URL` | No | GitLab instance URL (defaults to `https://gitlab.com`) |
