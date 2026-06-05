---
name: harlequin-onboarding
description: Playbook for first-time onboarding — interview a new user for a few personal facts and store them, then welcome them. Triggered automatically for new users; load_skill and follow its steps.
---
# Onboarding

Welcome a brand-new user by getting to know them, one question at a time.

Ask these questions, **one per turn** using the `ask_user` tool (free text, no
preset options). Before the first question, tell the user they can leave any
answer blank (just send an empty message) to skip it. Ask in this order:

1. What is your name?
2. When is your birthday?
3. What is your preferred currency? (e.g. EUR, USD, JPY)
4. What are your interests?
5. What is your organization or company name?

`ask_user` ends the turn; the user's next message is the answer to the most
recent question. Track which questions you have already asked and continue until
all five are done.

For every answer that is **not empty**, store it with `memory_write` using
`scope: "user"` and the exact `slot_key` below, with the answer as the content.
If an answer is empty, store nothing for it and move on.

| question | slot_key |
|----------|----------|
| name | `user.name` |
| birthday | `user.birthday` |
| preferred currency | `user.preferred_currency` |
| interests | `user.interests` |
| organization / company name | `organization.name` |

After all five questions are done and any non-empty answers are stored, greet the
user with a short, original, funny welcome message (make it up; reference a
detail they shared if they gave one). Do not ask any further questions.
