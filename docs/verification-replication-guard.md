# Verification — the replication collectors' guard

What was measured, on what, and what came back. Companion to
`docs/replication-spec.md`, section "How a collector guards itself".

Instrument: a throwaway container, SQL Server 2022, `@@VERSION` product version
`16.0.4265.3`, replication not configured. Database `guardpat_3c1f`, created for
this and dropped afterwards. No client instance was involved and none is named.

The floor is SQL Server 2012 and this was measured on 2022. What is measured
here is name-resolution and error-handling behaviour, unchanged across those
versions, but that is an argument rather than a measurement and the real
verification run should repeat these four on the floor.

## 1. A `WHERE` predicate does not guard a missing object

```sql
SET NOCOUNT ON;
SELECT 'root' AS marker, CAST(0 AS int) AS applies;
SELECT p.name FROM dbo.syspublications AS p WHERE 1 = 0;
```

```
marker applies
------ -----------
root             0
Msg 208, Level 16, State 1, Line 4
Invalid object name 'dbo.syspublications'.
```

The first result set arrives, then the batch aborts. A constant-false predicate
does not prevent the error: names are bound before predicates are evaluated. A
unit that fails this way has already emitted part of its output.

## 2. `TRY`/`CATCH` does not catch it, and everything after is lost

```sql
BEGIN TRY
    SELECT p.name FROM dbo.syspublications AS p WHERE 1 = 0;
END TRY
BEGIN CATCH
    SELECT ERROR_NUMBER() AS caught_number, ERROR_MESSAGE() AS caught_message;
END CATCH;
SELECT 'after' AS marker;
```

```
Msg 208, Level 16, State 1, Line 4
Invalid object name 'dbo.syspublications'.
```

No `caught_number` row, and no `after` row. The handler never ran and the batch
died at the statement.

## 3. The same read behind `sp_executesql` is caught

```sql
BEGIN TRY
    EXEC sp_executesql N'SELECT p.name FROM dbo.syspublications AS p WHERE 1 = 0';
END TRY
BEGIN CATCH
    SELECT ERROR_NUMBER() AS caught_number, LEFT(ERROR_MESSAGE(), 60) AS caught_message;
END CATCH;
SELECT 'after' AS marker;
```

```
caught_number caught_message
------------- ------------------------------------------
          208 Invalid object name 'dbo.syspublications'.

marker
------
after
```

Deferring name resolution to a lower execution level turns an uncatchable
compile error into a catchable runtime one, and execution continues.

## 4. The specified pattern, both branches

The pattern from the specification, run unchanged on a database that carries no
replication role:

```
applies     collected   error_number error_message
----------- ----------- ------------ -------------
          0           1            0 NULL

name  immediate_sync  allow_anonymous
----  --------------  ---------------
```

Two result sets, the second empty with its declared shape, no error.

The same file with the guard forced open, so the dynamic read runs against an
object that is not there:

```
applies collected error_number error_message
------- --------- ------------ -------------
      1         0          208 Invalid object name 'dbo.syspublications'.

name immediate_sync allow_anonymous
---- -------------- ---------------
```

Still two result sets. The failure is recorded in the root row rather than
destroying the unit.

## Not measured here

- Error 229 on a **permitted-but-refused** read of an object that exists. It is
  a runtime error and is caught by the same handler; one reviewer measured it
  on this container with a bare login and reported `229` returned as a row.
  Recorded as second-hand rather than as a measurement of ours.
- Whether a replication catalog view returns zero rows instead of raising 229
  for a login without rights. This needs a configured publisher and is the
  first question the topology verification run must answer, because the
  specification's three-state table depends on it.
- Everything requiring a configured topology: the distribution database's own
  tables, both history tables' contents, tracer tokens, and every column claim
  about them.
