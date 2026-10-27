# gh-court

Whose court is the ball in? `gh court` lists your open pull requests across
GitHub and says, for each one, whether it waits on **you**, on **them**, or has
gone **stale**, and why.

It is for people with many pull requests in many repositories. GitHub's
dashboard groups pull requests by review status. It does not notice a
maintainer's question, a check only a maintainer can clear, or CI that fails
for everyone.

```
gh extension install b84330808/gh-court
gh court
```

## Example

Real output for the author's 90 open pull requests (trimmed; the full run took
17 GraphQL requests and about 30 seconds):

```
Your turn · 7
    7d  mui/mui-x#23941              [data grid] Close the singleSelect editor menu on a backdrop click or Escape
        - CI failed on your head commit: deploy/netlify
        · a failed check only a maintainer can clear: check PR labels / Check PR type labels
    6d  microsoft/rushstack#6162     [rush-serve-plugin] Forward the auto-picked port to the port parameter
        - CI failed on your head commit: Node.js v26 (ubuntu-latest), Node.js v26 (windows-latest)
        · review requested from @iclanton, @octogonz, @apostolisms (+3 more) 6d ago
        · also failing on main, so probably not your change: Node.js v24 (windows-latest)
    4d  gorakhargosh/watchdog#1341   fix(core): hold the observer lock while starting emitters
        - merge conflict with master
        · 2 workflows wait for a maintainer to approve their runs: Tests, Build & Publish
   17h  plotly/plotly.js#8146        fix(pie): clear stale leader lines when outside labels move back inside
        - CI failed on your head commit: noci-jasmine
        · review requested from @KoolADE85 9h ago
        · some recent pull requests by others fail this too, so it may be flaky: noci-jasmine (2 of 5)
  …

Their turn · 81
   10d  VOICEVOX/voicevox_engine#1896  docs: エディタから起動したエンジンには127.0.0.1でアクセスするよう案内する
        - you replied last, 10d ago
    6d  mermaid-js/mermaid#8498      fix: require a state id after "as" in state diagrams
        - a failed check only a maintainer can clear: argos (1 changed — waiting for your decision)
    6d  tqdm/tqdm#1889               bar_format: show unknown rates as `?` with format specs
        - 2 workflows wait for a maintainer to approve their runs: Check, Test
        - review requested from @casperdcl 6d ago
        · also failing on recent pull requests by others, so probably not your change: pre-commit.ci - pr (4 of 8)
    2d  dagster-io/dagster#34334     [core] Fix DbIOManager for multi-partitioned assets with a dynamic dimension
        - you replied last, 2d ago
        · also failing on recent pull requests by others, so probably not your change: buildkite/unit-tests (8 of 8)
    2d  typeorm/typeorm#12996        fix: await async entity listeners declared in embeddeds
        - approved by @gioboa, @Cprakhar 2d ago, not merged yet
        - review requested from @smith-xyz, @alumni 2d ago
        · branch is behind master; protection requires it to be up to date
   19h  redis/redis-py#4507          Read JSON files as bytes in JSON.set_file
        - 4 workflows wait for a maintainer to approve their runs: CI, spellcheck, Docs CI (+1 more)
        - you pushed after @petyaslavova requested changes; waiting for re-review (review not re-requested from @petyaslavova)
        · 1 unresolved thread from bots
  …

Stale (no activity for 14+ days, consider a nudge) · 2
   4mo  pluja/awesome-privacy#865    Add WhisperPress to Speech to Text > Apps and services
        - waiting for a first review
        - no activity for 4mo
        nudge: leave a short, friendly ping or ask in the project's chat who reviews this area
```

Each group is sorted by how long the ball has been there, longest first.
Lines starting with `-` decided the turn; lines starting with `·` are
context. In a terminal the pull request names are links.

## Rules

**Your turn** when any of these is true:

| Code | Rule |
| --- | --- |
| `changes_requested` | Someone requested changes after your last push. |
| `unresolved_thread` | An unresolved review thread ends with someone else's comment, newer than your last push and your last reply in the conversation. |
| `ci_failed` | A check failed on your head commit, and it is not one of the cases below. |
| `conflict` | The pull request has a merge conflict. |
| `question` | After your last activity, someone asked a question or a favour ("could you…", "please…", `?`), or addressed you with an @mention that is more than a thank-you. Questions @-addressed to someone else do not count. |
| `draft` | The pull request is still a draft. |

**Their turn** otherwise:

| Code | Rule |
| --- | --- |
| `awaiting_approval` | GitHub Actions runs wait for a maintainer to approve them (first-time contributors from forks). |
| `maintainer_check` | A failed check only a maintainer can clear: label gates, approval gates, deploy previews that need authorization, visual diffs waiting for a decision. |
| `approved` | Approved by someone with write access, not merged. |
| `awaiting_rereview` | You pushed after changes were requested. Says when you have not re-requested the review. |
| `review_requested` | Reviews are requested from people or teams. |
| `awaiting_reply` | You wrote last. |
| `awaiting_review` | Nobody has reviewed yet, or nothing new happened since your last push. |

**Stale** is their turn with no activity from a person for `--stale-days`
(default 14). Stale rows get a suggested nudge. A stale pull request that waits
on you stays under "Your turn".

A failed check does not count against you when:

- the same check fails on the base branch's latest commit, or
- it fails on at least half (and at least 2) of the recent pull requests by
  other people in the same repository. gh-court looks at 8 of them
  (`--peers`). When fewer fail, the check stays yours with a "may be flaky"
  note.

Bots (GitHub Apps and accounts such as `codecov-commenter`, `CLAassistant`,
`coderabbitai`) are ignored except for a count of their unresolved threads.
`--bots` counts them like people.

## Reviewer mode

```
gh court --reviewer
```

lists pull requests by others where your review was requested, or where you
reviewed or commented. It is your turn when your review is requested (or
re-requested), the author pushed or replied after your last review or comment,
or someone mentioned you. `--teams` also includes requests sent to your teams.

```
Your turn · 2
    3d  sequelize/sequelize#18466  fix(core): clone ARRAY's subtype when cloning the DataType
        - @papandreou pushed 3d ago, after your last review or comment
        - @papandreou replied 3d ago: “…”
    3d  sequelize/sequelize#18545  fix(core): don't apply rejectOnEmpty to separate includes
        - @Mohamed-Elshesheny pushed 3d ago, after your last review or comment

Their turn (waiting on the author) · 1
    4d  sequelize/sequelize#18243  fix(core): recognise / as a valid named replacement terminator
        - you reviewed 4d ago; waiting on @arvi1999
```

## Output

- `--json` prints an array; every entry has `turn`, `since`, `lastActivity`,
  `reasons` and `notes` (each `{code, text, at, url}`) and `nudge`.
- `--markdown` prints one table per turn, for an issue or a notes file.
- `--turn you` (or `them`, `stale`, comma separated) filters.
- `--search 'org:cli'` adds search qualifiers.

```
gh court --json | jq -r '.[] | select(.turn == "you") | .url'
gh court --markdown --turn you,stale > waiting.md
```

## Cost and rate limits

gh-court only reads. One search request lists your pull requests, then
GraphQL `nodes()` queries fetch 8 pull requests each, 6 at a time
(`--batch`, `--parallel`). If GitHub times out on a batch, the batch is
split. One more request per 4 repositories with failing CI fetches the peer
checks. Each request costs 1 point of the 5,000-point hourly GraphQL budget.
gh-court stops before the budget falls below 200 points (`--min-rate-limit`)
and waits out secondary rate limits as GitHub asks.

## Known limits

- GitHub does not expose push times. The last push is estimated from the
  head commit date, the first check suite created for it, and force-push
  events.
- Question detection is a heuristic over English, Japanese and Chinese
  phrasing. A request without a question mark or a "please" can be missed.
  The newest other comment is always shown as a note.
- Only the last 30 comments, 30 reviews and 60 review threads per pull request
  are read.
- Mergeability is computed lazily by GitHub; a fresh pull request can show
  "not computed yet".

## Prior art

- [dlvhdr/gh-dash](https://github.com/dlvhdr/gh-dash) is a dashboard driven by
  search queries. Search queries cannot express "a maintainer asked something
  after my last push".
- [kaylendog/gh-turn](https://github.com/kaylendog/gh-turn) has the same idea
  and appeared while this was being written. gh-court adds failed-check
  triage (base branch, peers, maintainer-only checks), workflow approvals,
  unresolved threads, stale nudges, JSON and Markdown output.
- GitHub Attention Set (a browser extension) and Gerrit's attention set
  follow review events in the browser.

## Development

```
go test ./...
go test -run TestRecordedAccount -update .   # after an intended change; review the diff
gh court --record testdata/author            # re-record fixtures (comment bodies are redacted)
```

`testdata/` holds real GraphQL responses for the author's account with every
comment body replaced by neutral text that keeps only the signals gh-court
reads (question, request, thanks, mentions).

## License

MIT
