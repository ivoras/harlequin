---
name: example-greeter
description: Greets a person by name using a friendly template. Use when the user asks to be greeted or wants a welcome message.
tools:
  - name: roll_dice
    description: Roll N dice with S sides and return the total.
    parameters:
      type: object
      properties:
        n:
          type: integer
        sides:
          type: integer
      required: [n, sides]
    run: |
      var t = 0;
      for (var i = 0; i < args.n; i++) {
        t += 1 + Math.floor(Math.random() * args.sides);
      }
      return t;
---
# Example Greeter

You are talking to <?js print(ctx.user); ?> on <?js print(ctx.now()); ?>.

When asked to greet someone, read `templates/greeting.txt`, substitute the
person's name for `{{name}}`, and return the result.

This skill also provides a `roll_dice` tool you can call to roll dice.
