package github

// prFragment selects everything the turn inference needs from one pull
// request. Connection sizes are kept small: GitHub's GraphQL API spends about
// half a second per pull request on this fragment, so batches stay at around
// eight pull requests to keep each request well under the 10 second timeout.
const prFragment = `
fragment pr on PullRequest {
  id number title url isDraft createdAt updatedAt
  author { __typename login }
  repository { nameWithOwner isArchived }
  baseRefName
  baseRef { target { ... on Commit { statusCheckRollup { state contexts(first: 60) { nodes {
    __typename ... on CheckRun { name conclusion } ... on StatusContext { context state }
  } } } } } }
  mergeable mergeStateStatus reviewDecision
  reviewRequests(first: 20) {
    nodes { requestedReviewer { __typename ... on User { login } ... on Bot { login } ... on Mannequin { login } ... on Team { combinedSlug } } }
  }
  latestOpinionatedReviews(first: 20, writersOnly: true) {
    nodes { author { __typename login } authorAssociation state submittedAt url }
  }
  reviews(last: 30) {
    nodes { author { __typename login } authorAssociation state submittedAt body url }
  }
  comments(last: 30) {
    nodes { author { __typename login } authorAssociation createdAt body url }
  }
  reviewThreads(last: 60) {
    nodes {
      isResolved isOutdated path
      comments(last: 1) { nodes { author { __typename login } authorAssociation createdAt body url } }
    }
  }
  timelineItems(last: 40, itemTypes: [HEAD_REF_FORCE_PUSHED_EVENT, REVIEW_REQUESTED_EVENT, READY_FOR_REVIEW_EVENT]) {
    nodes {
      __typename
      ... on HeadRefForcePushedEvent { createdAt actor { __typename login } }
      ... on ReviewRequestedEvent { createdAt requestedReviewer { __typename ... on User { login } ... on Bot { login } ... on Mannequin { login } ... on Team { combinedSlug } } }
      ... on ReadyForReviewEvent { createdAt }
    }
  }
  commits(last: 1) {
    nodes { commit {
      oid committedDate
      author { user { login } }
      statusCheckRollup {
        state
        contexts(first: 60) { nodes {
          __typename
          ... on CheckRun { name conclusion status title detailsUrl checkSuite { workflowRun { workflow { name } } } }
          ... on StatusContext { context state description targetUrl }
        } }
      }
      checkSuites(first: 30) { nodes { createdAt status conclusion app { slug } workflowRun { url workflow { name } } } }
    } }
  }
}`

// searchQuery lists the IDs of the pull requests matching one search string,
// 100 per page. It is cheap; the details come from nodesQuery in batches.
const searchQuery = `
query($q: String!, $after: String) {
  viewer { login }
  search(query: $q, type: ISSUE, first: 100, after: $after) {
    issueCount
    pageInfo { hasNextPage endCursor }
    nodes { ... on PullRequest { id } }
  }
  rateLimit { cost remaining resetAt }
}`

const nodesQuery = `
query($ids: [ID!]!) {
  nodes(ids: $ids) { ...pr }
  rateLimit { cost remaining resetAt }
}` + prFragment
