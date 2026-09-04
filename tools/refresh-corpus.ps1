<#
.SYNOPSIS
  Regenerates testdata/corpus.txt from the embedded corpus, then runs the checks
  a commit has to pass.

.DESCRIPTION
  One command for the loop that follows adding or removing collectors. It exists
  because that loop used to be run one collector at a time: add a file, watch
  TestEmbeddedCorpusIsValid fail on a hardcoded count, edit the count, run
  again, discover the lint error the count had masked. Eight collectors cost
  something like sixteen round trips, and none of them was about the SQL.

  Two changes make it one command:

    * the inventory is testdata/corpus.txt rather than a number in a test, so
      it is regenerated instead of edited, and a failure names the file;
    * the inventory check reports with Errorf and no longer aborts the test, so
      the directive and contract lint run in the same pass. A new collector now
      fails once, with everything wrong with it listed together.

  NEVER RUN THIS IN CI, and ci.yml does not. The golden file is a guard: a
  collector must not enter or leave the corpus without someone saying so, and a
  pipeline that regenerated it before testing would assert nothing at all. The
  diff on testdata/corpus.txt is the thing a reviewer reads.

.EXAMPLE
  pwsh -File tools/refresh-corpus.ps1
  pwsh -File tools/refresh-corpus.ps1 -SkipTests   # regenerate only
#>
[CmdletBinding()]
param(
    [switch]$SkipTests
)

$ErrorActionPreference = 'Stop'
Set-Location (Join-Path $PSScriptRoot '..')

Write-Host '==> regenerating testdata/corpus.txt' -ForegroundColor Cyan
go test . -run TestEmbeddedCorpusIsValid -update -v
if ($LASTEXITCODE -ne 0) { throw 'the corpus did not load; nothing was regenerated' }

# Shown rather than summarised: the reviewer's question is which collector
# arrived or left, and that is exactly what this prints.
if (Get-Command git -ErrorAction SilentlyContinue) {
    $diff = git diff --stat -- testdata/corpus.txt
    if ($diff) {
        Write-Host '==> the inventory changed' -ForegroundColor Yellow
        git --no-pager diff -- testdata/corpus.txt
    } else {
        Write-Host '==> the inventory is unchanged' -ForegroundColor Green
    }
}

if ($SkipTests) { return }

Write-Host '==> gofmt' -ForegroundColor Cyan
$unformatted = gofmt -l .
if ($unformatted) {
    # The working tree is CRLF under core.autocrlf, so gofmt -l flags files it
    # would not touch on a checkout with LF endings. CI runs on Linux and is the
    # authority; this is a hint, not a verdict.
    Write-Host 'not gofmt-clean (check the endings before believing it):' -ForegroundColor Yellow
    $unformatted
}

Write-Host '==> go vet' -ForegroundColor Cyan
go vet ./...
if ($LASTEXITCODE -ne 0) { throw 'go vet failed' }

Write-Host '==> go test' -ForegroundColor Cyan
go test ./... -count=1
if ($LASTEXITCODE -ne 0) { throw 'go test failed' }

Write-Host '==> green' -ForegroundColor Green
