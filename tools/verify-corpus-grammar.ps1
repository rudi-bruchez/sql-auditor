<#
.SYNOPSIS
  Parses every file in queries/ under the T-SQL grammar matching its own
  @min_version directive, and checks the declared @resultsets count against the
  number of top-level SELECT statements in the parse tree.

.DESCRIPTION
  docs/verification-2012.md claims the corpus is written to the SQL Server 2012
  grammar. This script is that claim's evidence: it is the thing a reader runs
  to check it, rather than a sentence they have to take on trust. Its output is
  committed alongside as docs/verification-2012-parse.txt.

  It is a static check and nothing more. It proves the SQL parses under the
  older grammar and that the result-set count matches what the runner will
  expect. It cannot prove a DMV exists on 2012, that a column returns the
  expected type, or that SERVERPROPERTY knows a name — SERVERPROPERTY returns
  NULL for an unknown name rather than erroring, so no parser can catch that.
  Executing against a real 2012 instance is the only thing that can, and that
  is what the checklist in docs/verification-2012.md is for.

.EXAMPLE
  pwsh -File tools/verify-corpus-grammar.ps1
  pwsh -File tools/verify-corpus-grammar.ps1 -QueriesDir ./queries
#>
[CmdletBinding()]
param(
    [string]$QueriesDir = (Join-Path $PSScriptRoot '..' 'queries'),
    [string]$ScriptDomPath
)

$ErrorActionPreference = 'Stop'

# ScriptDom ships inside SSMS, Visual Studio's SQL tooling and the
# Microsoft.SqlServer.TransactSql.ScriptDom NuGet package. Take the first one
# found so the script runs without a build step; -ScriptDomPath overrides.
if (-not $ScriptDomPath) {
    $roots = @($env:ProgramFiles, ${env:ProgramFiles(x86)}, (Join-Path $HOME '.nuget')) |
        Where-Object { $_ -and (Test-Path $_) }
    $ScriptDomPath = Get-ChildItem -Path $roots -Recurse -Filter 'Microsoft.SqlServer.TransactSql.ScriptDom.dll' `
        -ErrorAction SilentlyContinue | Select-Object -First 1 -ExpandProperty FullName
}
if (-not $ScriptDomPath) {
    throw 'Microsoft.SqlServer.TransactSql.ScriptDom.dll not found. Install SSMS, the SQL Server Data Tools, or the Microsoft.SqlServer.TransactSql.ScriptDom NuGet package, or pass -ScriptDomPath.'
}
Add-Type -Path $ScriptDomPath

# The parser is chosen from the file's own @min_version, not fixed at 110. A
# gated file must parse under the grammar it declares; using 110 for all of them
# would fail the 2016 files for reasons that are not defects, and using the
# newest for all of them would accept 2016 syntax in an ungated file and let the
# 2012 floor rot silently.
$parserMap = @{
    11 = @('TSql110Parser', 'TSql110 (SQL Server 2012)')
    12 = @('TSql120Parser', 'TSql120 (SQL Server 2014)')
    13 = @('TSql130Parser', 'TSql130 (SQL Server 2016)')
    14 = @('TSql140Parser', 'TSql140 (SQL Server 2017)')
    15 = @('TSql150Parser', 'TSql150 (SQL Server 2019)')
    16 = @('TSql160Parser', 'TSql160 (SQL Server 2022)')
    17 = @('TSql170Parser', 'TSql170 (SQL Server 2025)')
}

# Returns the parser and its label, or $null and the reason it could not be
# built. It does NOT throw, and that is the point.
#
# It used to throw on an unmapped version, and the map stopped at 15. When
# 073.accelerators.sql arrived declaring @min_version: 16, the run died on it —
# at file 073 of the corpus, in alphabetical order — and every file after it
# went unchecked. The exception looked like a broken tool rather than an
# unverified corpus, so nothing was verified for as long as it took to notice.
#
# One gated file must never stop the corpus being checked. An unmapped version
# is now one failing line among the others, which is loud in the right place:
# the file is reported, the run continues, and the exit code is still non-zero.
#
# The type is resolved by name at run time because ScriptDom builds differ. A
# machine whose SSMS predates SQL Server 2022 has no TSql160Parser, and the
# honest answer there is "this build cannot check that file", not a crash.
function Get-ParserFor([string]$minVersion) {
    $major = 11                       # no @min_version means the 2012 floor
    if ($minVersion -match '^(\d+)') { $major = [int]$Matches[1] }

    $entry = $parserMap[$major]
    if (-not $entry) {
        return $null, "no parser mapped for @min_version '$minVersion'"
    }
    $type = "Microsoft.SqlServer.TransactSql.ScriptDom.$($entry[0])" -as [type]
    if (-not $type) {
        return $null, "$($entry[0]) is not in this ScriptDom build ($ScriptDomPath)"
    }
    return $type::new($true), $entry[1]
}

$files = Get-ChildItem -Path $QueriesDir -Recurse -Filter '*.sql' | Sort-Object FullName
if (-not $files) { throw "no .sql files under $QueriesDir" }

# The recorded output is an artifact someone will read months later, so it has
# to say which corpus it describes. It identifies that by the git tree object of
# queries/, not by HEAD.
#
# HEAD cannot work. The artifact is committed one commit after the run that
# produced it, so a HEAD stamp is always the commit before the one containing
# the file — which is exactly how the first version of this artifact came to
# contradict the document citing it. Amending does not help either: amending
# changes the commit id, so the stamp goes stale again.
#
# The tree object of queries/ has neither problem. It changes when and only when
# the corpus changes, it is unaffected by commits that touch anything else, and
# a reader can check the artifact still describes the current corpus with a
# single command that needs no history:
#
#     git rev-parse HEAD:queries
$corpusTree = try { (git rev-parse 'HEAD:queries' 2>$null) } catch { $null }
if (-not $corpusTree) { $corpusTree = '(not a git checkout)' }

"sql-auditor corpus grammar check"
"================================"
"Produced    : $((Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ'))"
"Corpus tree : $corpusTree"
"              (git rev-parse HEAD:queries - this artifact describes that corpus"
"               and no other; if the two differ, re-run this script)"
"ScriptDom   : $ScriptDomPath"
"Files       : $($files.Count)"
""

$failures = 0
foreach ($f in $files) {
    $text = Get-Content -Raw -LiteralPath $f.FullName
    $rel = [IO.Path]::GetRelativePath((Resolve-Path (Join-Path $QueriesDir '..')).Path, $f.FullName) -replace '\\', '/'

    $minVersion = ''
    if ($text -match '(?m)^--\s*@min_version:\s*(\S+)') { $minVersion = $Matches[1] }

    # @resultsets is a comma-separated list of name:kind pairs.
    $declared = 0
    if ($text -match '(?m)^--\s*@resultsets:\s*(.+)$') {
        $declared = ($Matches[1] -split ',' | Where-Object { $_.Trim() }).Count
    }

    $parser, $grammar = Get-ParserFor $minVersion
    if (-not $parser) {
        # $grammar carries the reason here, not a label.
        "{0,-46} {1,-28} resultsets {2}/{3}  {4}" -f $rel, '(no parser)', '-', $declared, 'NOT CHECKED'
        "      $grammar"
        $failures++
        continue
    }
    $errors = $null
    $reader = [IO.StringReader]::new($text)
    $fragment = $parser.Parse($reader, [ref]$errors)
    $reader.Dispose()

    # Top-level SELECTs are what the runner reads back as result sets. Counting
    # from the parse tree rather than from the directive is the whole point: a
    # mismatch here is the runner's "returned N result sets but @resultsets
    # declares M" failure, caught before anyone runs it against a server.
    #
    # Two shapes of SELECT send nothing to the client and must not be counted.
    # SELECT @v = ... assigns a variable; 050.tempdb.sql opens with one to count
    # the CPUs, and counting it reported a 12/11 mismatch against a file that
    # runs correctly. SELECT ... INTO writes a table. Both are still SELECT
    # statements to the parser.
    $actual = 0
    if ($fragment) {
        foreach ($batch in $fragment.Batches) {
            foreach ($st in $batch.Statements) {
                if ($st -isnot [Microsoft.SqlServer.TransactSql.ScriptDom.SelectStatement]) { continue }
                $spec = $st.QueryExpression -as [Microsoft.SqlServer.TransactSql.ScriptDom.QuerySpecification]
                if ($spec) {
                    if ($spec.Into) { continue }
                    $assignments = @($spec.SelectElements | Where-Object {
                        $_ -is [Microsoft.SqlServer.TransactSql.ScriptDom.SelectSetVariable]
                    })
                    if ($assignments.Count -eq @($spec.SelectElements).Count) { continue }
                }
                $actual++
            }
        }
    }

    $status = 'ok'
    if ($errors.Count -gt 0) { $status = 'PARSE ERRORS'; $failures++ }
    elseif ($actual -ne $declared) { $status = 'RESULTSET MISMATCH'; $failures++ }

    "{0,-46} {1,-28} resultsets {2}/{3}  {4}" -f $rel, $grammar, $actual, $declared, $status
    foreach ($e in $errors) { "      line {0}: {1}" -f $e.Line, $e.Message }
}

""
if ($failures -gt 0) { "FAILED: $failures file(s)"; exit 1 }
"All $($files.Count) files parse under their declared grammar, and every"
"@resultsets count matches the parse tree."
exit 0
