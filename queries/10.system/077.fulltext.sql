-- @scope:       instance
-- @resultsets:  root:object
-- @permissions: CONNECT
-- @timeout:     30
--
-- Full-Text service installation state and the two security-relevant
-- properties of that service.
--
-- Why this collector exists. The SQL Server Assessment ruleset has two rules
-- on the Full-Text service — whether it loads operating-system resources, and
-- whether it verifies signatures — and both are meaningless until the service
-- itself is known to be installed. FULLTEXTSERVICEPROPERTY answers all three
-- in one place and asks for no permission beyond CONNECT.
--
-- THE VERDICT ORDER IS INSTALL FIRST, PROPERTIES SECOND. When the service is
-- not installed, IsFullTextInstalled carries the answer (0) and the two other
-- properties return NULL — which is itself the correct reading: there is no
-- service to load OS resources or verify signatures. The collector succeeds
-- in that state by design; a NULL here is the expected shape of "not
-- installed", not a collection failure.
--
-- THE PROPERTIES ARE PROJECTED AS BITS because each is a 0/1 answer and a
-- topic should not have to reverse-engineer the type of a property function.
-- NULL survives the CAST, so the not-installed shape above is preserved.
--
-- NO JUDGEMENT IS APPLIED. What the two properties should be on a given
-- instance is a ruleset question; this file only reports what they are.

SET NOCOUNT ON;
SET TRANSACTION ISOLATION LEVEL READ UNCOMMITTED;
SET LOCK_TIMEOUT 10000;

SELECT CONVERT(varchar(23), SYSDATETIME(), 126)                   AS [collected_at],
       CAST(FULLTEXTSERVICEPROPERTY('IsFullTextInstalled') AS bit) AS [installed],
       CAST(FULLTEXTSERVICEPROPERTY('LoadOSResources') AS bit)     AS [load_os_resources],
       CAST(FULLTEXTSERVICEPROPERTY('VerifySignature') AS bit)     AS [verify_signature]
OPTION (RECOMPILE, MAXDOP 1);
