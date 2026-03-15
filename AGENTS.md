# Agent Instructions

Use `bd` for task tracking.

## Autonomous Worker Mode

When spawned as a worker agent, follow this loop:

### Work Loop

1. **Pull latest and find work:**
   ```bash
   cd ~/projects/ai/dgt
   git pull --rebase
   bd ready
   ```
   **If the list is empty or all tasks are claimed → STOP** (see Stopping Conditions).

2. **Pick a task** from the `bd ready` output — choose the lowest task ID that you
   haven't already tried to claim this session. (`bd ready` only shows unclaimed,
   unblocked tasks; you don't need to filter further.)

3. **Claim it atomically:**
   ```bash
   bd update <task-id> --claim
   bd update <task-id> --status=in_progress
   ```
   If `--claim` fails (someone else claimed it first), return to step 1.

4. **Create your worktree** (use the claimed task ID):
   ```bash
   # Guard against leftover worktrees from crashed sessions
   git worktree list | grep <task-id> && echo "WARNING: worktree exists — check for prior session"
   git worktree add .worktrees/<task-id> -b agent/<task-id>
   cd .worktrees/<task-id>
   ```
   For multiple tasks in one session: use `agent/<id1>-<id2>` as the branch name.

5. **Understand context:**
   - Read `bd show <task-id>` for full description
   - Read existing code in related packages

6. **Implement:**
   - Write the code
   - Follow Google style guide for Go
   - Keep changes minimal and focused

7. **Verify:**
   ```bash
   go build ./...
   go test ./...
   ```

8. **Commit and merge** (from inside the worktree):
   ```bash
   # Commit your work
   git add <changed files>
   git commit -m "feat(<task-id>): <description>

   Co-Authored-By: Claude Sonnet 4.6 <noreply@anthropic.com>"

   # Rebase onto latest main, then verify the merged result
   git fetch origin
   git rebase origin/main
   go build ./...
   go test ./...

   # Switch to main worktree and merge
   cd ~/projects/ai/dgt
   git pull --rebase --autostash
   git merge --ff-only agent/<task-id>
   git push
   ```
   If `--ff-only` fails (another agent pushed while you were rebasing):
   ```bash
   cd .worktrees/<task-id>
   git rebase origin/main   # resolve any conflicts
   go build ./... && go test ./...
   cd ~/projects/ai/dgt
   git pull --rebase --autostash
   git merge --ff-only agent/<task-id>
   git push
   ```
   Clean up the worktree after a successful push:
   ```bash
   git worktree remove --force .worktrees/<task-id>
   git branch -d agent/<task-id>
   ```

9. **Close the task** (only after push succeeds):
   ```bash
   bd close <task-id> --reason "Implemented <brief description>"
   bd dolt commit
   ```

10. **Loop** — go back to step 1, check for more ready work.

---

### Pre-Assigned Mode (Recommended for Parallel Agents)

To avoid claiming races entirely, spawn each agent with a specific task ID:

```bash
# Terminal 1
claude "You are worker-1. Read AGENTS.md. Claim <task-id-1>, implement it, close it, then check bd ready for more work."

# Terminal 2
claude "You are worker-2. Read AGENTS.md. Claim <task-id-2>, implement it, close it, then check bd ready for more work."

# Terminal 3
claude "You are worker-3. Read AGENTS.md. Claim <task-id-3>, implement it, close it, then check bd ready for more work."
```

After completing their assigned task, agents continue with the work loop above.

---

### Stopping Conditions

Stop when ANY of these occur:
- `bd ready` returns an empty list
- Every listed task already has an assignee (all claimed by other agents)
- **Context usage reaches 80%** — run `/context` to check

**When stopping mid-task at 80% context:**
1. Finish the current atomic operation (don't leave code half-written)
2. `go build ./...` to verify it compiles
3. Follow step 8 to commit, merge, and push what you have
4. Record progress:
   ```bash
   bd update <task-id> --description="PROGRESS: <what's done, what remains>"
   ```
   Or close if fully complete (step 9).
5. **STOP** — let the user spawn a fresh agent to continue.

---

### Conflict Avoidance

**Task-level:** `bd update --claim` is atomic — only one agent can claim a task.
Retry with a different task if it fails; don't poll the same task.

**File-level:** Tasks are designed to touch different files.
Do NOT edit files outside your task's scope.

**Git-level:** Each agent works in `.worktrees/<task-id>` (gitignored), fully isolated.
Commit frequently — small commits reduce conflict surface.

---

### Discovering New Work

Never write `// TODO` or `// FIXME` in code. Instead:

- **Quick fix (<2 min)?** Do it now.
- **Larger work (>2 min)?** File a bd issue and continue your current task:
  ```bash
  bd create "Issue title" \
    --description="What needs to be done" \
    -t <bug|task|feature|chore> \
    -p <1-2> \
    --deps discovered-from:<current-task-id>
  ```

---

## Non-Interactive Shell Commands

Always use non-interactive flags to avoid hanging on confirmation prompts
(some systems alias `cp`/`mv`/`rm` to interactive mode):

```bash
cp -f source dest      # not: cp source dest
mv -f source dest      # not: mv source dest
rm -f file             # not: rm file
rm -rf directory       # not: rm -r directory
```

Other: `apt-get -y`, `ssh -o BatchMode=yes`, `scp -o BatchMode=yes`.

---

## Issue Tracking Quick Reference

```bash
bd ready                        # unclaimed, unblocked tasks
bd show <id>                    # full issue details
bd update <id> --claim          # atomic claim (fails if already claimed)
bd update <id> --status=in_progress
bd close <id> --reason "..."    # mark complete
bd dolt commit                  # flush writes to Dolt history
```

Issue types: `bug`, `feature`, `task`, `epic`, `chore`
Priorities: `0`=critical `1`=high `2`=medium `3`=low `4`=backlog

---

## Landing the Plane (Session Completion)

Work is NOT complete until `git push` succeeds.

1. File bd issues for any remaining/discovered work
2. Quality gates: `go build ./... && go test ./...`
3. Close finished tasks (step 9), update in-progress ones
4. Push:
   ```bash
   git pull --rebase --autostash
   bd dolt commit
   git push
   git status   # must show "up to date with origin"
   ```
5. Clean up: `git worktree list` — remove any leftover worktrees

**YOU must push. Never say "ready to push when you are."**
