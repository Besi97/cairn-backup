# ADR 0004: Schedule syntax and clock behavior

- Status: Accepted
- Date: 2026-11-15
- Issue: [#55](https://github.com/Besi97/cairn-backup/issues/55)

## Context

Configuration v1 reserved `cron` without choosing a dialect. Define and validate
the contract before implementing the agent, so configuration checking can catch
schedule mistakes without Borg or repository access.

## Decision

Use five-field cron with ordinary wildcards, lists, ranges, steps, named months
and weekdays, and calendar aliases from `@hourly` through `@yearly`. Sunday is 0;
restricted day-of-month and day-of-week fields use OR semantics. Reject seconds,
interval schedules, startup aliases, Quartz extensions, and inline timezone
directives. Use robfig/cron's parser with a restricted syntax wrapper, not its
scheduling loop.

Add optional workflow `timezone`, accepting IANA names or UTC and defaulting to
the agent process local timezone. Embed timezone data for minimal installations.
Validate cron and timezones in the shared loader, preserving offline validation
and safe diagnostics. The JSON Schema checks their types and nonempty strings;
it does not duplicate the parser or timezone database.

Skip nonexistent DST times; repeated local times may run twice at distinct real
instants. Do not catch up missed occurrences, replay already-dispatched real
occurrences after backward clock changes, or retry warnings, failures, or
overlaps. An occurrence whose minute has expired is missed, not queued.

The full dialect is documented in [Configuration](../configuration.md#schedules).
Scheduling never changes Borg retention or UTC archive naming.

## Consequences

Invalid schedules now fail configuration loading, including for manual commands
that validate the entire selected file. Calendar feasibility is not validated;
the parser's next-time search is bounded to five years.

The foreground `cairn agent run` command and clock-driven dispatch remain #80.
Workflow and agent locks remain #81. This decision does not implement either.
