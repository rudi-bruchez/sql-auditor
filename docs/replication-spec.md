# Replication collection — specification

Status: draft, not implemented. Written 3 September 2026.

## What it is for

An audit of a server that publishes, distributes or subscribes has one question
behind all the others: **is the topology keeping up, and what is it costing the
publisher?** A log that will not truncate, a Log Reader that has been failing
for three weeks, a publication nobody removed but whose retention still pins
transactions in the distribution database — these are findings, and today the
archive cannot support any of them.

What it holds instead is four flags. `90.availability/040.replication.sql`
reports `is_published`, `is_merge_published`, `is_subscribed` and
`is_distributor` from `sys.databases`, and says in its own header that the
useful metadata is out of reach. That header is honest and it is also the
problem this specification closes.

## What is already collected, and stays collected

Nothing below replaces work that exists. Three collectors already carry part of
the answer and the new files must not duplicate them:

- `50.agent/010.jobs.sql` reports every SQL Agent job with its category and its
  last outcome. Replication runs as jobs in the `REPL-LogReader`,
  `REPL-Distribution` and `REPL-Snapshot` categories, and a failing agent is
  the most common replication finding in an audit. This file already finds it.
- `20.databases/024.log-stats.sql` reports `log_reuse_wait_desc` beside the log
  size and the VLF count. A publisher whose log is held by `REPLICATION` is
  visible there today.
- `90.availability/040.replication.sql` reports the flags, which remain the
  cheapest way to see a database that was restored from a publisher and kept a
  flag nobody cleared.

The new work is the layer under those three: which publications exist, on what
terms, and how far behind their agents are.

## The targeting problem, and the rule that solves it

Publication and subscription metadata lives in the published and subscribed
databases, both of which are ordinary user databases already in the selection.
Distribution metadata lives in **the distribution database**, whose name is
chosen when replication is configured. `sp_adddistributiondb` defaults it to
`distribution`, and nothing obliges anyone to accept that default.

The name is not, in fact, the hard part. The distribution database has a
`database_id` greater than 4, so `CandidateDatabases` already lists it and a
database-scoped collector already runs against it — on a run with no
`DB_INCLUDE`. The gap is narrower than the current header claims, and it is
this: **an operator who narrows the run to the application database loses the
distributor that goes with it.**

So the rule is a second pass over the selection, not a third scope:

> A database carrying `is_distributor = 1` is retained even when `DB_INCLUDE`
> does not match it, provided at least one database **retained after
> filtering** carries `is_published = 1`. `DB_EXCLUDE` still wins, and so do
> the state and access checks.

Three properties make this a rule rather than a convenience:

**It only fires where it is needed.** With no `DB_INCLUDE`, every user database
is already a candidate and the pass changes nothing. It exists for one
situation: a narrowed run whose narrowed set still contains a publisher.

**It cannot fire on a server that is only a distributor.** A remote distributor
serving publishers on other instances has no locally published database, so a
narrowed run collects nothing extra. That is correct — the operator who wants
that server's distribution database can name it, and on an unnarrowed run it is
collected anyway.

**The operator keeps a way to say no.** `DB_EXCLUDE` is checked after the pass,
so a client who will not have the distribution database read has an explicit
lever, and the manifest records the exclusion by name.

The retention is recorded, not silent. The manifest carries a reason beside the
database — the counterpart of the `SkipReason` entries it already writes — so a
reader can see that a database was collected because a published database was
retained, rather than wonder why a database they did not ask for appears in the
archive.

### The publisher whose distributor is elsewhere

When a database is published and no local database carries `is_distributor`,
the distributor is remote and none of this applies. The archive should say so
rather than leave an absence to interpret: `sys.servers` carries an
`is_distributor` flag, which names the distributor without a stored procedure
and without a hard-coded database name. This goes in `040.replication.sql`,
which is already instance-scoped and already the file a reader consults first.

## The three collectors

One per role, each `@scope: database`, each self-guarded on the flag of the
database it is running in.

### `90.availability/041.replication-publisher.sql`

Guarded on `is_published`. Reads the publication database's own tables:

- `dbo.syspublications` — name, `status`, `repl_freq`, `sync_method`,
  `retention`, `allow_push`, `allow_pull`, `allow_anonymous`,
  `immediate_sync`, `independent_agent`.
- `dbo.sysarticles` — one row per published article with its destination
  object, so the audit can say how much of the database is replicated.
- `dbo.syssubscriptions` — who subscribes, push or pull, and the
  synchronization status.

`immediate_sync` and `allow_anonymous` are the reason this file exists rather
than being folded into the distributor collector. Together they make the
distribution database keep every command for the full retention period whether
or not a subscriber has taken it, which is the ordinary explanation for a
publisher log that will not truncate and a distribution database that only
grows. Read beside `024.log-stats.sql`, they turn `log_reuse_wait =
REPLICATION` from a symptom into a cause.

### `90.availability/042.replication-distribution.sql`

Guarded on `is_distributor`. This is the file the specification is really for.

**Configuration**, from `msdb.dbo.MSdistributiondbs`: `min_distretention`,
`max_distretention`, `history_retention`. This table is in `msdb`, so it is
readable without knowing the distribution database's name, and it bounds the
meaning of everything else — an agent history reaching back six hours is not
evidence of a young topology if `history_retention` is six hours.

**Topology**, from the distribution database: `MSpublications` (publisher,
publisher database, publication, `publication_type`, `retention`,
`immediate_sync`), `MSarticles`, and `MSsubscriptions` for the publication to
subscriber mapping. `publication_type` distinguishes transactional from
snapshot and merge, so a merge publication on a server audited for
transactional replication is visible even though its own metadata is out of
scope.

**Agents**, from `MSdistribution_agents`, `MSlogreader_agents` and
`MSsnapshot_agents`: name, publication, subscriber, and the job behind them.

**Latency**, aggregated over a bounded window from `MSdistribution_history` and
`MSlogreader_history`. Both tables carry `agent_id`, `runstatus`, `start_time`,
`time`, `duration`, `comments`, `delivery_latency` and `delivered_commands`.
The two latencies measure different legs and the archive must not merge them:
in `MSlogreader_history`, `delivery_latency` is the milliseconds between a
command being committed in the published database and arriving in the
distribution database; in `MSdistribution_history` it is the milliseconds
between the distribution database and the subscriber. A topology that is behind
is behind on one leg or the other, and which one it is decides where to look.

Per agent, the archive gets the most recent session with its `runstatus` and
its time, the latest `delivery_latency`, and the maximum and median
`delivery_latency` over the window. `runstatus` is stored as its code and its
label together — 1 start, 2 succeed, 3 in progress, 4 idle, 5 retry, 6 fail —
because a code alone sends every reader back to the documentation and a label
alone cannot be matched on.

**Errors**, from `MSrepl_errors`: the last N rows in the window, with the
message truncated. `error_id` in the history tables joins to it, so a failing
agent's own error is retrievable rather than merely counted.

**Tracer tokens**, from `MStracer_history` and `MStracer_tokens`, read only if
they contain anything. See the next section.

### `90.availability/043.replication-subscriber.sql`

Guarded on `is_subscribed`. Reads `dbo.MSreplication_subscriptions` —
publisher, publisher database, publication, `subscription_type` (0 push, 1
pull, 2 anonymous), the distribution agent's name, and `Time`, the last update
the Distribution Agent made — and `dbo.MSsubscription_agents` for the agent's
own view.

On a pull subscription this file is the only place the topology is visible at
all: the agent runs on the subscriber, and its history lives on the
distributor, which may be a server the audit never touches. `Time` going stale
is then the whole signal.

## The read-only promise is a design constraint

The manifest of every archive says the collector issues only read-only SELECT
statements, runs no INSERT, UPDATE, DELETE or DDL, and reads no user or
application table. `observe` exists as a separate command because
`CREATE EVENT SESSION` is DDL and a promise with one exception is not a
promise. The same reasoning binds here, and it costs something real:

**No `sp_posttracertoken`.** Posting a tracer token is the sanctioned way to
measure end-to-end latency, and it is a write. It is not done. Where a client
already posts tokens on a schedule, `MStracer_history` holds the measurements
and they are collected for free; where nobody does, the archive says the
history is empty rather than manufacturing a number.

**Catalog tables and views, not `sp_replmonitor*` procedures.** The monitor
procedures are the documented path and they are read-only in practice, but the
shape of a procedure's result set is not a contract, several of them require
membership in `replmonitor`, and some build temporary tables. The corpus reads
catalog objects everywhere else for these reasons and does so here too.

**No `MSdistribution_status`.** The view exposes `UndelivCmdsInDistDB`, the
count of commands pending delivery per article and agent, and it is the number
that most directly explains a growing distribution database. It is deliberately
not collected: it aggregates `MSrepl_commands`, the largest table on any busy
distributor, and an audit collector must not be the reason a distributor
stalls. The size of that table is available for nothing from
`sys.dm_db_partition_stats`, and its row count answers the same question well
enough to open a conversation.

## Permissions

`VIEW ANY DEFINITION` does not reach any of this. The distribution database's
tables need read access inside that database, granted either directly or
through the `replmonitor` role; the publication database's `syspublications`
and `sysarticles` are readable by `db_owner` and by members of the publication
access list, not by a bare reader.

The posture is **recommended grant, graceful degradation**:

1. `docs/dba-guide.md` documents the grant to request, what it opens, and what
   is lost without it, so a DBA can decide before the run rather than discover
   the gap in the archive.
2. `check` reports the missing right before collection, while there is still
   time to ask for it, and `check --grant-script` emits the corresponding
   statements.
3. Each collector wraps its reads in `TRY`/`CATCH` and, on refusal, emits
   `collected = 0` with the error number and message, exactly as
   `50.agent/010.jobs.sql` does for `sysjobhistory`. A refusal is recorded in
   the archive, never silent.

The third point matters most, and the reason is in the history of this
repository: an earlier replication collector read `msdb.dbo.sysjobs` and was
refused on the audit login it was written for. A collector that assumes its
grant is a collector that returns nothing on half the instances it runs
against.

## Shape of the output

Every collector declares its result sets in `@resultsets`, and the root of each
is a single-row object. **The guard must not skip statements.** A script that
wraps its SELECTs in `IF @is_distributor = 1` emits a different number of
result sets depending on the server, which the archive writer cannot honour.
The guard is a predicate in the `WHERE` clause, so a database that does not
carry the flag produces the declared result sets with no rows, and the root row
is always emitted, carrying an explicit `applies` field.

This also gives the analysis layer something to match on: `applies = 0` on
every database means the instance replicates nothing, which is a different
statement from a collector that failed.

## Cost

The window over the history tables is the only tunable, and it defaults to
seven days on the model of `QUERY_STORE_DAYS`. Bounding it matters less for
volume than for meaning — `history_retention` may already be shorter — but a
distributor that has run for years with a long retention holds enough history
rows to make an unfiltered aggregate a bad neighbour.

Every statement carries `OPTION (RECOMPILE, MAXDOP 1)` and
`SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED`, as the auditor contract
requires. No `COUNT(*)` runs against `MSrepl_commands`.

## Changes to existing code

**`collect.DatabaseInfo`** gains `IsPublished`, `IsSubscribed` and
`IsDistributor`; `CandidateDatabases` projects them.

**`collect.SelectTargets`** gains the second pass described above and a
retention reason on the selection, which the manifest renders.

**`90.availability/040.replication.sql`** has its header rewritten. Most of it
argues that distribution metadata is unreachable, and that argument stops being
true when this is implemented. What remains true and stays: the flags are
flags, not proof of activity, and a database restored from a publisher keeps
them. What replaces the rest: a pointer to the three new files, and the
`sys.servers` reading that tells a publisher its distributor is remote.

**`docs/dba-guide.md`** gains the grant, its justification, and a sentence on
the `DB_INCLUDE` widening — an operator who narrowed a run and finds an extra
database in the archive must be able to look up why.

## What this does not do

**Merge replication.** `sysmergepublications`, `sysmergearticles` and the
conflict tables are a different metadata model and a different set of audit
questions. `MSpublications.publication_type = 2` makes a merge publication
visible on the distributor, which is enough to say it exists and that the audit
did not examine it.

**Peer-to-peer and updatable subscriptions.** `allow_sync_tran`,
`allow_queued_tran` and the Queue Reader's history are read where they sit in
tables already being read, but no collector is written for them.

**Any judgement.** No latency is called high, no retention is called wrong, no
failing agent is called a problem. A Log Reader failing for a month may be a
decommissioned publication nobody removed, and the archive is not the place
that decides. This matches every other collector in the corpus.

**Replication on Azure SQL Database and managed instances.** Out of scope for
the same reason the rest of the corpus targets SQL Server 2012 and later on
instances the auditor can reach.

## Tests and verification

Go tests cover what Go does: the second pass in `SelectTargets` — retained when
a published database survives filtering, not retained when none does, excluded
when `DB_EXCLUDE` names it, skipped when offline or inaccessible — and the
manifest line that explains the retention. The corpus lint covers the three new
files' directives and their conformance to the auditor contract.

The SQL itself is not covered by an automated test and the specification should
not pretend otherwise. `compose.yaml` runs a single SQL Server 2022 container;
configuring transactional replication inside it, with a publisher, a
distributor and a subscriber, is a substantial piece of work whose return is
one integration test. Verification is manual against a real topology and
recorded on the model of `docs/verification-2012.md`: the query, the server
version, the result, and what was refused — with no proper nouns, per
`CLAUDE.md`.

The floor stays SQL Server 2012. Every table and column named here is
long-standing, so no `@minversion` directive is expected on any of the three
files; the verification run is what confirms that rather than this paragraph.

## Open questions

**Should the widening extend to `is_merge_published`?** Today it does not,
because merge is out of scope. If a merge collector is ever written, the rule
needs one word changed, and the decision belongs to that moment rather than to
this one.

**How many rows of `MSrepl_errors`?** N is left unset here. It wants to be
small enough to bound the archive and large enough that a repeating failure
shows as a repetition. Fifty is the likely answer and the verification run
should confirm it against a distributor that has actually been failing.

**Does the distribution database deserve its own exclusion from the schema
collectors?** `70.schema/010.objects.sql` will now run against it on narrowed
runs and report several hundred replication system objects, which is noise in
an object inventory. Leaving it is defensible — the volumetry of
`MSrepl_commands` is genuinely useful — but that is a decision to take
deliberately after the first real run, not now.
