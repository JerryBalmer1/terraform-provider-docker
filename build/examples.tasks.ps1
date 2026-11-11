# Example tests (Pass 6b Lane E): ExampleTests (every examples/**/<name>.tests.ps1 through Pester on the dind harness),
# ExampleCoverage (every registered type has an example and a test, or an allowlist entry), ExampleModuleTest (the
# test/TfDockerTest module's own Pester tests, no harness) and ManualTaskCheck (no gate reaches an underscore task).
# Dot-sourced by terraform-provider-docker.build.ps1, so it shares that script's variables and helpers.

$ExampleRunner = Join-Path $BuildRoot 'test/TfDockerTest/Invoke-ExamplePester.ps1'
$ExampleModuleTests = Join-Path $BuildRoot 'test/TfDockerTest/TfDockerTest.Tests.ps1'
$ExampleAllowlist = Join-Path $BuildRoot 'reference/_gen/deferred.d/examples.json'
# registry_gen.go kind => examples/ folder (the tfplugindocs layout).
$ExampleKindDirs = [ordered]@{
    resource           = 'resources'
    data_source        = 'data-sources'
    ephemeral_resource = 'ephemeral-resources'
    action             = 'actions'
    function           = 'functions'
    list_resource      = 'list-resources'
}

# Every registered type as (Kind, Name, Example), from the `// <kind> <name>` comments of internal/provider/registry_gen.go.
function Get-RegisteredExampleTypes {
    $path = Join-Path $BuildRoot 'internal/provider/registry_gen.go'
    foreach ($m in [regex]::Matches([IO.File]::ReadAllText($path), '(?m)^\s*New\w+,\s*//\s*(\w+)\s+(\S+)\s*$')) {
        $kind = $m.Groups[1].Value
        if (-not $ExampleKindDirs.Contains($kind)) { throw "registry_gen.go: unknown kind '$kind'" }
        [pscustomobject]@{ Kind = $kind; Name = $m.Groups[2].Value; Example = "$($ExampleKindDirs[$kind])/$($m.Groups[2].Value)" }
    }
}

# Problems with example coverage: a registered type without an example folder (a .tf / .tfquery.hcl file) and a
# <name>.tests.ps1 beside it and without a whole-example allowlist row; an example folder no registered type owns;
# an allowlist row for a folder that does not exist. Pure function of its inputs (ExampleCoverage self-tests it).
function Test-ExampleCoverage {
    param(
        [Parameter(Mandatory)][object[]]$Types,
        [Parameter(Mandatory)][string]$ExamplesDir,
        [object[]]$Allowlist = @()
    )
    $problems = [System.Collections.Generic.List[string]]::new()
    $whole = @{}
    foreach ($a in $Allowlist) {
        if (-not (Test-Path -LiteralPath (Join-Path $ExamplesDir $a.example) -PathType Container)) { $problems.Add("allowlist row '$($a.example)': no such folder under examples/") }
        if (-not $a.reason) { $problems.Add("allowlist row '$($a.example)': no reason") }
        if (-not ($a.PSObject.Properties['part'] -and $a.part)) { $whole[$a.example] = $true }
    }
    $owned = @{ provider = $true }
    foreach ($t in $Types) {
        $owned[$t.Example] = $true
        $dir = Join-Path $ExamplesDir $t.Example
        $hasConfig = (Test-Path -LiteralPath $dir -PathType Container) -and @(Get-ChildItem -LiteralPath $dir -File | Where-Object { $_.Name -like '*.tf' -or $_.Name -like '*.tfquery.hcl' }).Count -gt 0
        $hasTest = $hasConfig -and @(Get-ChildItem -LiteralPath $dir -File -Filter '*.tests.ps1').Count -gt 0
        if ($hasConfig -and $hasTest) { continue }
        if ($whole.ContainsKey($t.Example)) { continue }
        $problems.Add("$($t.Kind) $($t.Name): " + $(if (-not $hasConfig) { "no example in examples/$($t.Example)" } else { "no <name>.tests.ps1 in examples/$($t.Example)" }) + ' (and no allowlist row in reference/_gen/deferred.d/examples.json)')
    }
    if (Test-Path -LiteralPath $ExamplesDir) {
        foreach ($cat in Get-ChildItem -LiteralPath $ExamplesDir -Directory) {
            if ($cat.Name -eq 'provider') { continue }
            foreach ($d in Get-ChildItem -LiteralPath $cat.FullName -Directory) {
                if (-not $owned.ContainsKey("$($cat.Name)/$($d.Name)")) { $problems.Add("examples/$($cat.Name)/$($d.Name): no registered type of that name") }
            }
        }
        if (-not @(Get-ChildItem -LiteralPath (Join-Path $ExamplesDir 'provider') -Filter '*.tests.ps1' -ErrorAction Ignore).Count) { $problems.Add('examples/provider: no provider.tests.ps1') }
    }
    $problems
}

# Synopsis: Every registered type (registry_gen.go) has an example folder and a <name>.tests.ps1, or a reference/_gen/deferred.d/examples.json row; no harness. (Pass 6b Lane E)
task ExampleCoverage {
    $types = @(Get-RegisteredExampleTypes)
    $allow = @()
    if (Test-Path -LiteralPath $ExampleAllowlist) { $allow = @(([IO.File]::ReadAllText($ExampleAllowlist) | ConvertFrom-Json).examples) }

    # Self-test on a throwaway tree, so the check cannot silently pass because it is broken.
    $tmp = Join-Path $TpdWork "excov-$PID"
    try {
        $null = New-Item -ItemType Directory -Force (Join-Path $tmp 'resources/docker_a'), (Join-Path $tmp 'resources/docker_b'), (Join-Path $tmp 'resources/docker_orphan'), (Join-Path $tmp 'provider')
        Set-Content (Join-Path $tmp 'resources/docker_a/resource.tf') 'x'
        Set-Content (Join-Path $tmp 'resources/docker_a/docker_a.tests.ps1') 'x'
        Set-Content (Join-Path $tmp 'resources/docker_b/resource.tf') 'x'
        Set-Content (Join-Path $tmp 'provider/provider.tests.ps1') 'x'
        $fake = @([pscustomobject]@{ Kind = 'resource'; Name = 'docker_a'; Example = 'resources/docker_a' }, [pscustomobject]@{ Kind = 'resource'; Name = 'docker_b'; Example = 'resources/docker_b' }, [pscustomobject]@{ Kind = 'resource'; Name = 'docker_c'; Example = 'resources/docker_c' })
        $got = @(Test-ExampleCoverage -Types $fake -ExamplesDir $tmp)
        assert ($got.Count -eq 3 -and ($got -match 'docker_b: no <name>.tests.ps1') -and ($got -match 'docker_c: no example') -and ($got -match 'docker_orphan')) "ExampleCoverage self-test failed: $($got -join '; ')"
        $got = @(Test-ExampleCoverage -Types $fake -ExamplesDir $tmp -Allowlist @([pscustomobject]@{ example = 'resources/docker_b'; reason = 'r' }))
        assert ($got.Count -eq 2 -and -not ($got -match 'docker_b')) "ExampleCoverage self-test (allowlist) failed: $($got -join '; ')"
    } finally { Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction Ignore }

    $problems = @(Test-ExampleCoverage -Types $types -ExamplesDir (Join-Path $BuildRoot 'examples') -Allowlist $allow)
    foreach ($p in $problems) { Write-Build Red "  $p" }
    $byKind = $types | Group-Object Kind | ForEach-Object { "$($_.Name) $($_.Count)" }
    $parts = @($allow | Where-Object { $_.PSObject.Properties['part'] -and $_.part }).Count
    Write-Build Gray ("registered: {0}; allowlist rows: {1} whole, {2} parts" -f ($byKind -join ', '), ($allow.Count - $parts), $parts)
    assert ($problems.Count -eq 0) "ExampleCoverage: $($problems.Count) problem(s)"
    Write-Build Green "ExampleCoverage: all $($types.Count) registered types have an example and a test"
}

# Synopsis: Pester tests of the test/TfDockerTest module itself (terraform and the Engine API mocked; no harness). (Pass 6b Lane E)
task ExampleModuleTest {
    requires -Path $ExampleModuleTests
    $ps = (Get-Process -Id $PID).Path
    $script = "Import-Module Pester -MinimumVersion 5.0; `$c = New-PesterConfiguration; `$c.Run.Path = '$ExampleModuleTests'; `$c.Run.Exit = `$true; `$c.Output.Verbosity = 'Normal'; Invoke-Pester -Configuration `$c"
    exec { & $ps -NoProfile -Command $script }
}

# Synopsis: Every example test as a dry run, no harness: its edited copy (Replace, RemoveBlock, Files) through terraform init + validate with the local provider. (Pass 6b Lane E)
# Harness variables get placeholder values, so the edits apply as on the harness. TPD_EXAMPLE_INCLUDE limits it as for ExampleTests.
task ExampleValidate {
    requires -Path $ExampleRunner
    $placeholders = [ordered]@{
        TPD_EXAMPLE_DRYRUN = '1'; TF_ACC_REGISTRY = '127.0.0.1:12380'; TF_ACC_PLUGIN = '127.0.0.1:12380/tfacc/noop-volume:1'
        TF_ACC_PLUGIN_DIR = (Join-Path $BuildRoot 'test/harness/tfaccplugin/volume'); TF_ACC_CSI_PLUGIN = 'tfacc-csi'
        TF_ACC_CONTAINERD_HOST = 'tcp://127.0.0.1:12383'; TF_ACC_TLS_HOST = 'tcp://127.0.0.1:12378'
        TF_ACC_TLS_CA = 'ca.pem'; TF_ACC_TLS_CERT = 'cert.pem'; TF_ACC_TLS_KEY = 'key.pem'
        DOCKER_WORKER1_HOST = 'tcp://127.0.0.1:12376'; DOCKER_WORKER2_HOST = 'tcp://127.0.0.1:12377'
        E2E_MANAGER_ADDR = 'manager:2377'; E2E_ADVERTISE_ADDR = 'eth0'
    }
    $saved = @{}
    foreach ($k in $placeholders.Keys) { $saved[$k] = [Environment]::GetEnvironmentVariable($k); [Environment]::SetEnvironmentVariable($k, $placeholders[$k]) }
    try {
        $ps = (Get-Process -Id $PID).Path
        $runnerArgs = @('-NoProfile', '-File', $ExampleRunner)
        $sel = Get-ExampleSelection
        if ($sel) { $runnerArgs += @('-Include', $sel) }
        exec { & $ps @runnerArgs }
    } finally {
        foreach ($k in $saved.Keys) { [Environment]::SetEnvironmentVariable($k, $saved[$k]) }
    }
}
# The example selection: -Filter, else TPD_EXAMPLE_INCLUDE (comma-separated wildcards on examples/<kind>/<name> or <name>).
function Get-ExampleSelection {
    $sel = if ($Filter) { $Filter } elseif ($env:TPD_EXAMPLE_INCLUDE) { $env:TPD_EXAMPLE_INCLUDE } else { '' }
    (@($sel -split ',' | ForEach-Object { $_.Trim() } | Where-Object { $_ }) -join ',')
}

# The selected example test files, each with whether it belongs to the swarm pass (its Describe carries tag 'swarm').
function Get-ExampleTestFiles([string]$Selection) {
    $examples = Join-Path $BuildRoot 'examples'
    $pats = @($Selection -split ',' | Where-Object { $_ })
    foreach ($f in Get-ChildItem -LiteralPath $examples -Recurse -File -Filter '*.tests.ps1' | Sort-Object FullName) {
        $rel = [IO.Path]::GetRelativePath($examples, $f.DirectoryName).Replace('\', '/')
        $leaf = Split-Path -Leaf $rel
        if ($pats.Count -and -not @($pats | Where-Object { $rel -like $_ -or ($_ -notmatch '/' -and $leaf -like $_) }).Count) { continue }
        $describe = @([IO.File]::ReadAllLines($f.FullName) | Where-Object { $_ -match '^\s*Describe\b' }) -join "`n"
        [pscustomobject]@{ Example = $rel; Swarm = ($describe -match "-Tag\b[^`n]*'swarm'") }
    }
}

# One line per test case of the NUnit XML files: (Example, Result, Message).
function Read-ExampleResults([string[]]$XmlFiles) {
    foreach ($x in $XmlFiles | Where-Object { $_ -and (Test-Path -LiteralPath $_) }) {
        $doc = [xml][IO.File]::ReadAllText($x)
        foreach ($tc in $doc.SelectNodes('//test-case')) {
            $msg = ''
            $m = $tc.SelectSingleNode('failure/message')
            if ($m) { $msg = (($m.InnerText -split "`r?`n") | Where-Object { $_.Trim() } | Select-Object -First 1) }
            # Pester's test-case name is "<Describe>.<It>", and the Describe is the example path (examples/<kind>/<name>).
            $name = $tc.GetAttribute('name')
            $ex = if ($name -match '^(examples/[A-Za-z0-9_/-]+)') { $Matches[1] } else { $name }
            [pscustomobject]@{ Example = $ex; Result = $tc.GetAttribute('result'); Message = $msg }
        }
    }
}

# Synopsis: Every example's Pester test on the dind harness (Invoke-E2E.ps1 -Script): plain pass, then -SwarmInit pass for tag 'swarm'; one summary by example name. -Filter re-runs a subset. (Pass 6b Lane E)
# One harness per pass (not per example), one provider build and one terraform plugin cache for the whole run; each
# example has its own 5-minute budget (TPD_EXAMPLE_TIMEOUT_MIN) plus 3 minutes for teardown, so one hang fails that
# example only. Examples run one at a time: each sweeps every object that appeared on the daemon since it started, so
# two at once would remove each other's objects. A failing example is reported by name with its error and does not
# stop the others. Results go to $env:TPD_EXAMPLE_RESULTS (default test/harness/.logs/examples, git-ignored):
# examples-<pass>-<stamp>.xml (NUnit), the pass log, one terraform log per example under logs-<pass>-<stamp>/, and
# examples-summary-<stamp>.txt. A pass with no selected example is skipped (no harness). Never in the default task.
task ExampleTests {
    requires -Path $Harness, $ExampleRunner
    requires -Variable GoExe
    $out = if ($env:TPD_EXAMPLE_RESULTS) { $env:TPD_EXAMPLE_RESULTS } else { Join-Path $BuildRoot 'test/harness/.logs/examples' }
    $null = New-Item -ItemType Directory -Force $out
    $stamp = Get-Date -Format 'yyyyMMdd-HHmmss'
    $selection = Get-ExampleSelection
    $files = @(Get-ExampleTestFiles $selection)
    assert ($files.Count) "ExampleTests: no example test matches '$selection'"
    # One provider mirror for every example: the runner builds it (go build, host platform, zipped under the registry
    # path layout) on the first pass and the second reuses it through TPD_PROVIDER_MIRROR; every example runs a real
    # terraform init from it (lock file, checksum verified). The plugin cache is shared.
    $saved = @{ TPD_PROVIDER_MIRROR = $env:TPD_PROVIDER_MIRROR; TF_PLUGIN_CACHE_DIR = $env:TF_PLUGIN_CACHE_DIR }
    $env:TPD_PROVIDER_MIRROR = Join-Path $out "mirror-$stamp"
    $env:TF_PLUGIN_CACHE_DIR = Join-Path $out 'plugin-cache'
    $null = New-Item -ItemType Directory -Force $env:TF_PLUGIN_CACHE_DIR
    $codes = [ordered]@{}
    $xmls = @()
    $started = Get-Date
    try {
        foreach ($pass in 'plain', 'swarm') {
            $n = @($files | Where-Object { $_.Swarm -eq ($pass -eq 'swarm') }).Count
            if (-not $n) { Write-Build Gray "ExampleTests $pass pass: no selected example, skipped"; continue }
            $xml = Join-Path $out "examples-$pass-$stamp.xml"
            $log = Join-Path $out "examples-$pass-$stamp.log"
            $logs = Join-Path $out "logs-$pass-$stamp"
            $xmls += $xml
            $scriptArgs = @('-OutputXml', $xml, '-LogDir', $logs) + $(if ($pass -eq 'swarm') { @('-Tag', 'swarm') } else { @('-ExcludeTag', 'swarm') })
            if ($selection) { $scriptArgs += @('-Include', $selection) }
            Write-Build Cyan "ExampleTests $pass pass ($n examples): NUnit XML $xml, log $log, terraform logs $logs"
            $passStart = Get-Date
            $code = 0
            try {
                $global:LASTEXITCODE = 0
                if ($pass -eq 'swarm') { & $Harness -SwarmInit -Script $ExampleRunner -ScriptArgs $scriptArgs -Log $log }
                else { & $Harness -Script $ExampleRunner -ScriptArgs $scriptArgs -Log $log }
                $code = $LASTEXITCODE
            } catch { Write-Warning "ExampleTests $pass pass: $_"; $code = 1 }
            $codes[$pass] = $code
            Write-Build Gray ("ExampleTests {0} pass: exit {1}, {2:mm\:ss}" -f $pass, $code, ((Get-Date) - $passStart))
        }
    } finally {
        foreach ($k in $saved.Keys) { [Environment]::SetEnvironmentVariable($k, $saved[$k]) }
    }
    $results = @(Read-ExampleResults $xmls)
    $failed = @($results | Where-Object { $_.Result -notin 'Success', 'Ignored', 'Skipped', 'Inconclusive' })
    $lines = @(
        ('ExampleTests {0}: selected {1}, passed {2}, failed {3}, skipped {4}, {5:hh\:mm\:ss}' -f $stamp, $files.Count,
            @($results | Where-Object Result -EQ 'Success').Count, $failed.Count,
            @($results | Where-Object Result -In 'Ignored', 'Skipped').Count, ((Get-Date) - $started))
        foreach ($f in $failed) { "FAILED $($f.Example): $($f.Message)" }
        foreach ($p in $codes.Keys) { if ($codes[$p] -and -not @($failed).Count) { "FAILED $p pass: exit $($codes[$p]) with no failed test case (harness or runner error: see examples-$p-$stamp.log)" } }
    )
    $summary = Join-Path $out "examples-summary-$stamp.txt"
    Set-Content -LiteralPath $summary -Value $lines
    foreach ($l in $lines) { Write-Build $(if ($l -like 'FAILED*') { 'Red' } else { 'Gray' }) $l }
    Write-Build Gray "ExampleTests results: $out (summary $summary)"
    assert (-not ($codes.Values | Where-Object { $_ })) "ExampleTests failed (plain pass exit $($codes['plain']), swarm pass exit $($codes['swarm']))"
}

# ---------------------------------------------------------------------------- manual-task guard

# Paths from the roots to an underscore task through the job graph ($Graph: task name => jobs; a job is a task name,
# '?<name>' for a safe reference, or a script block). Returns 'root -> ... -> _Task' for each one found.
function Find-ManualTaskReach {
    param([Parameter(Mandatory)][System.Collections.IDictionary]$Graph, [Parameter(Mandatory)][string[]]$Roots)
    $hits = [System.Collections.Generic.List[string]]::new()
    $seen = @{}
    $walk = {
        param([string]$Name, [string]$Trail)
        if ($Name.StartsWith('_')) { $hits.Add($Trail); return }
        if ($seen.ContainsKey($Name)) { return }
        $seen[$Name] = $true
        if (-not $Graph.Contains($Name)) { return }
        foreach ($j in @($Graph[$Name])) {
            if ($j -isnot [string]) { continue }
            $n = $j.TrimStart('?')
            & $walk $n "$Trail -> $n"
        }
    }
    foreach ($r in $Roots) { & $walk $r $r }
    $hits
}

# Task names invoked by `Invoke-Build ...` in the given workflow files (a bare `Invoke-Build` is the default task '.').
function Get-WorkflowBuildTasks([string[]]$Files) {
    foreach ($f in $Files) {
        foreach ($line in [IO.File]::ReadAllLines($f)) {
            $t = $line.Trim()
            if ($t.StartsWith('#') -or $t -match '^-?\s*name:') { continue }
            foreach ($m in [regex]::Matches($t, '(?<![\w-])Invoke-Build\b(?<rest>[^#]*)')) {
                $rest = $m.Groups['rest'].Value.Trim()
                $names = @()
                foreach ($tok in ($rest -split '\s+')) {
                    if (-not $tok -or $tok.StartsWith('-') -or $tok -match '^[|;&>]') { break }
                    $names += @($tok.Split(',') | ForEach-Object { $_.Trim().Trim("'", '"') } | Where-Object { $_ })
                }
                if (-not $names.Count) { $names = @('.') }
                $names
            }
        }
    }
}

# Synopsis: Fail if the default task, PreRelease ($PreReleaseTasks) or any Invoke-Build call in .github/workflows/*.yml reaches an underscore (manual) task. (Pass 6b Lane E)
task ManualTaskCheck {
    $graph = @{}
    foreach ($k in ${*}.All.Keys) { $graph[$k] = @(${*}.All[$k].Jobs) }

    # Negative self-tests: a synthetic graph where a gate reaches _Apply, and a workflow line calling _Up, must be caught.
    $bad = @(Find-ManualTaskReach -Graph @{ '.' = @('Build', '?Gate'); Gate = @('_Apply'); Build = @({ 'x' }) } -Roots '.')
    assert ($bad.Count -eq 1 -and $bad[0] -eq '. -> Gate -> _Apply') "ManualTaskCheck self-test failed: $($bad -join '; ')"
    $null = New-Item -ItemType Directory -Force $TpdWork
    $tmp = Join-Path $TpdWork "mtc-$PID.yml"
    try {
        Set-Content -LiteralPath $tmp -Value "      - name: Invoke-Build (default task)`n        run: Invoke-Build`n      - run: Invoke-Build _Up, Check -Scenario x`n      # run: Invoke-Build _Down"
        $wf = @(Get-WorkflowBuildTasks $tmp)
        assert (($wf -join ',') -eq '.,_Up,Check') "ManualTaskCheck workflow-parse self-test failed: $($wf -join ',')"
        $bad = @(Find-ManualTaskReach -Graph @{ '.' = @('Build') } -Roots $wf)
        assert ($bad -contains '_Up') "ManualTaskCheck self-test failed: a workflow calling _Up was not caught ($($bad -join '; '))"
    } finally { Remove-Item -LiteralPath $tmp -ErrorAction Ignore }

    $workflows = @(Get-ChildItem -LiteralPath (Join-Path $BuildRoot '.github/workflows') -Filter *.yml -File -ErrorAction Ignore | ForEach-Object FullName)
    $ci = @(Get-WorkflowBuildTasks $workflows | Select-Object -Unique)
    $roots = @(@('.', 'PreRelease') + @($PreReleaseTasks) + $ci | Select-Object -Unique)
    $hits = @(Find-ManualTaskReach -Graph $graph -Roots $roots)
    foreach ($h in $hits) { Write-Build Red "  $h" }
    $manual = @($graph.Keys | Where-Object { $_.StartsWith('_') } | Sort-Object)
    Write-Build Gray ("roots: {0}; manual tasks: {1}" -f ($roots -join ', '), ($manual -join ', '))
    assert ($hits.Count -eq 0) "ManualTaskCheck: $($hits.Count) gate path(s) reach an underscore task (manual tasks never run in the default task, PreRelease or CI)"
    Write-Build Green 'ManualTaskCheck: no gate reaches an underscore task'
}
