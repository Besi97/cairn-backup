# Recovery selection integration

Selection and revalidation only list the repository; they do not modify it.
They are intended for cooperative workflows where the repository and access
configuration remain stable, not for adversarial concurrent mutation.

Resolve `LatestPrefix` immediately before the recovery attempt, not when a
schedule or job is created. Retain the returned byte-exact name and both IDs;
never fall back to another archive if a check fails. Revalidate that candidate
using the same prepared repository, access settings, and invocation directory
immediately before extraction and again after extraction, before reporting
success or using the extracted data. A missing archive, changed ID, Borg
warning, or any other error fails the attempt. Do not repeat latest-prefix
selection during revalidation: a newly created later archive does not change
the identity of the candidate already selected.

The current #134 `workspace.Run` accepts `Options.ArchiveName`, not a selector
or IDs, and calls its consumer after extraction. A downstream caller must pass
the candidate name unchanged and run the post-extraction check inside the
consumer before accessing the extracted data; propagate any check failure.
It must also run the pre-extraction check immediately before `Run`, and use the
same repository for all operations. `Run` itself performs separate preflight
listing, tar export, and extraction commands. Before/after identity checks
cannot guarantee that the same archive was preflighted and extracted, nor can
they detect replacement followed by restoration of the original. Stronger
identity guarantees require an appropriate lock spanning all operations or a
trusted immutable repository snapshot; this API provides neither.

Selection accepts literal names containing `::` or braces, including names
with arbitrary non-UTF-8 bytes. #134's restore preparation rejects `::` and
braces in `ArchiveName` and builds a Borg location from the name. Fail with a
clear unsupported-name error for these candidates; do not substitute display
text, reinterpret a prefix, or escape them without verified Borg semantics.
Path selectors are separate: #134 accepts literal `Paths`, converts them to
`pp:` selectors, and requires either nonempty paths or `WholeArchive`, not
both. Archive selection does not select paths.
