package github

import "time"

// Actor is the author of a comment, review or event. Typename is "User",
// "Bot", "Mannequin" or "Organization".
type Actor struct {
	Typename string `json:"__typename"`
	Login    string `json:"login"`
}

// Reviewer is a requested reviewer: a user, bot, mannequin or team.
type Reviewer struct {
	Typename     string `json:"__typename"`
	Login        string `json:"login"`
	CombinedSlug string `json:"combinedSlug"`
}

// Name is "@login" for people and "@org/team" for teams.
func (r *Reviewer) Name() string {
	if r == nil {
		return ""
	}
	if r.Typename == "Team" {
		return "@" + r.CombinedSlug
	}
	return "@" + r.Login
}

type Review struct {
	Author            *Actor    `json:"author"`
	AuthorAssociation string    `json:"authorAssociation"`
	State             string    `json:"state"`
	SubmittedAt       time.Time `json:"submittedAt"`
	Body              string    `json:"body"`
	URL               string    `json:"url"`
}

type Comment struct {
	Author            *Actor    `json:"author"`
	AuthorAssociation string    `json:"authorAssociation"`
	CreatedAt         time.Time `json:"createdAt"`
	Body              string    `json:"body"`
	URL               string    `json:"url"`
}

type ReviewThread struct {
	IsResolved bool   `json:"isResolved"`
	IsOutdated bool   `json:"isOutdated"`
	Path       string `json:"path"`
	Comments   struct {
		Nodes []Comment `json:"nodes"`
	} `json:"comments"`
}

type TimelineItem struct {
	Typename          string    `json:"__typename"`
	CreatedAt         time.Time `json:"createdAt"`
	Actor             *Actor    `json:"actor"`
	RequestedReviewer *Reviewer `json:"requestedReviewer"`
}

type CheckContext struct {
	Typename string `json:"__typename"`
	// CheckRun
	Name       string `json:"name"`
	Conclusion string `json:"conclusion"`
	Status     string `json:"status"`
	Title      string `json:"title"`
	DetailsURL string `json:"detailsUrl"`
	CheckSuite *struct {
		WorkflowRun *struct {
			Workflow struct {
				Name string `json:"name"`
			} `json:"workflow"`
		} `json:"workflowRun"`
	} `json:"checkSuite"`
	// StatusContext
	Context     string `json:"context"`
	State       string `json:"state"`
	Description string `json:"description"`
	TargetURL   string `json:"targetUrl"`
}

// Rollup is a commit's combined CI status.
type Rollup struct {
	State    string `json:"state"`
	Contexts struct {
		Nodes []CheckContext `json:"nodes"`
	} `json:"contexts"`
}

// CheckName is the check run name or status context.
func (c CheckContext) CheckName() string {
	if c.Typename == "StatusContext" {
		return c.Context
	}
	return c.Name
}

// Workflow is the GitHub Actions workflow of a check run, if any.
func (c CheckContext) Workflow() string {
	if c.CheckSuite != nil && c.CheckSuite.WorkflowRun != nil {
		return c.CheckSuite.WorkflowRun.Workflow.Name
	}
	return ""
}

// Failed reports whether the check failed.
func (c CheckContext) Failed() bool {
	if c.Typename == "StatusContext" {
		return c.State == "FAILURE" || c.State == "ERROR"
	}
	switch c.Conclusion {
	case "FAILURE", "TIMED_OUT", "STARTUP_FAILURE":
		return true
	}
	return false
}

type CheckSuite struct {
	CreatedAt  time.Time `json:"createdAt"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion"`
	App        *struct {
		Slug string `json:"slug"`
	} `json:"app"`
	WorkflowRun *struct {
		URL      string `json:"url"`
		Workflow struct {
			Name string `json:"name"`
		} `json:"workflow"`
	} `json:"workflowRun"`
}

type Commit struct {
	OID           string    `json:"oid"`
	CommittedDate time.Time `json:"committedDate"`
	Author        *struct {
		User *struct {
			Login string `json:"login"`
		} `json:"user"`
	} `json:"author"`
	StatusCheckRollup *Rollup `json:"statusCheckRollup"`
	CheckSuites       struct {
		Nodes []CheckSuite `json:"nodes"`
	} `json:"checkSuites"`
}

// PullRequest mirrors the prFragment selection.
type PullRequest struct {
	ID         string    `json:"id"`
	Number     int       `json:"number"`
	Title      string    `json:"title"`
	URL        string    `json:"url"`
	IsDraft    bool      `json:"isDraft"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
	Author     *Actor    `json:"author"`
	Repository struct {
		NameWithOwner string `json:"nameWithOwner"`
		IsArchived    bool   `json:"isArchived"`
	} `json:"repository"`
	BaseRefName string `json:"baseRefName"`
	BaseRef     *struct {
		Target *struct {
			StatusCheckRollup *Rollup `json:"statusCheckRollup"`
		} `json:"target"`
	} `json:"baseRef"`
	Mergeable        string `json:"mergeable"`
	MergeStateStatus string `json:"mergeStateStatus"`
	ReviewDecision   string `json:"reviewDecision"`
	ReviewRequests   struct {
		Nodes []struct {
			RequestedReviewer *Reviewer `json:"requestedReviewer"`
		} `json:"nodes"`
	} `json:"reviewRequests"`
	LatestOpinionatedReviews struct {
		Nodes []Review `json:"nodes"`
	} `json:"latestOpinionatedReviews"`
	Reviews struct {
		Nodes []Review `json:"nodes"`
	} `json:"reviews"`
	Comments struct {
		Nodes []Comment `json:"nodes"`
	} `json:"comments"`
	ReviewThreads struct {
		Nodes []ReviewThread `json:"nodes"`
	} `json:"reviewThreads"`
	TimelineItems struct {
		Nodes []TimelineItem `json:"nodes"`
	} `json:"timelineItems"`
	Commits struct {
		Nodes []struct {
			Commit Commit `json:"commit"`
		} `json:"nodes"`
	} `json:"commits"`
}

// BaseRollup is the CI status of the base branch's latest commit, or nil.
func (p *PullRequest) BaseRollup() *Rollup {
	if p.BaseRef == nil || p.BaseRef.Target == nil {
		return nil
	}
	return p.BaseRef.Target.StatusCheckRollup
}

// Head returns the head commit, or nil when the pull request has none.
func (p *PullRequest) Head() *Commit {
	if len(p.Commits.Nodes) == 0 {
		return nil
	}
	return &p.Commits.Nodes[0].Commit
}

// RateLimit is the rateLimit block every query asks for.
type RateLimit struct {
	Cost      int       `json:"cost"`
	Remaining int       `json:"remaining"`
	ResetAt   time.Time `json:"resetAt"`
}
