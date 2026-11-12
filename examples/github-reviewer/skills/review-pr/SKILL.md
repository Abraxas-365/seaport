---
name: review-pr
description: Use when asked to review a pull request (or when called on a pull request without a more specific request)
---

Review the pull request named in the conversation title (`PR owner/repo#N: …`).

1. **Read it.**
   - `gh pr view N -R owner/repo --json title,body,author,headRefOid,files,commits`
   - `gh pr diff N -R owner/repo`
   - Read the discussion so far (`gh pr view N -R owner/repo --comments`); don't repeat what someone already said.

2. **Understand the change.** Check out the branch (`gh pr checkout N -R owner/repo`) and read the changed code in context, not just the diff: callers, tests, docs the change should update.

3. **Run what you can.** Find the project's commands (README, AGENTS.md, Makefile, package.json, CI workflow) and run the relevant ones: build, lint, the tests of the changed packages. Note each command and its result. If something can't run here (missing service, secrets), say so.

4. **Write the findings.** For each: file, line, what goes wrong, a fix. Mark each one **blocking** or **optional**.

5. **Post the review** at the head commit, with line comments:

   ```sh
   cat > /tmp/review.json <<'EOF'
   {
     "commit_id": "<headRefOid>",
     "event": "REQUEST_CHANGES",
     "body": "Summary of the review: what changed, what you ran, the verdict.",
     "comments": [
       {"path": "src/cart.ts", "line": 42, "side": "RIGHT",
        "body": "**Blocking:** the total ignores the coupon.\n```suggestion\nreturn subtotal - coupon.amount\n```"}
     ]
   }
   EOF
   gh api repos/owner/repo/pulls/N/reviews --input /tmp/review.json
   ```

   `event` is `APPROVE`, `COMMENT` or `REQUEST_CHANGES`. Line comments must point at lines that are part of the diff; put anything else in the body.

6. **Reply in the thread** with the `reply` tool: the verdict in one line, the blocking findings as a short list, and a link to the review.

7. Go back to the default branch (`git checkout -`).
