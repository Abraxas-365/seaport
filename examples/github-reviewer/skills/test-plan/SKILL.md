---
name: test-plan
description: Use when asked how to test a change, or for a QA or manual test plan of a pull request
---

Write a manual test plan someone can follow in under 15 minutes.

1. Read the change (`gh pr view N -R owner/repo --json title,body,files` and `gh pr diff N -R owner/repo`) and find what a user or caller would notice: screens, endpoints, commands, messages.
2. List the cases, the riskiest first:
   - the main path the change is for,
   - edge cases the code handles (empty, too long, wrong permissions, concurrent use),
   - what the change could break nearby (callers of a changed function, a shared component).
3. For each case write the steps and the expected result, with concrete data:

   | # | Steps | Expected |
   | :--- | :--- | :--- |
   | 1 | Sign in as a member, open Orders, apply coupon `TEST10` | Total drops 10%, coupon shown under the total |

4. Say what you already checked yourself (commands run, their result) and what still needs a person, an environment or real data.
5. Reply with the plan in the thread. If it's long, attach it as a file: `seaport reply <conversation_id> -file test-plan.md "Test plan attached"`.
