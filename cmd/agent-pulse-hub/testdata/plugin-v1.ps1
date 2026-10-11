$ErrorActionPreference = 'Stop'
[Console]::InputEncoding = [Text.UTF8Encoding]::new($false)
[Console]::OutputEncoding = [Text.UTF8Encoding]::new($false)
# A non-null handler consumes this process's Ctrl+C; it does not alter the
# inheritable console-ignore attribute of the real daemon.
[Reflection.Assembly]::LoadFrom($env:APH_FIXTURE_CONSOLE_DLL) | Out-Null
[FixtureConsole]::Install()
$root = [IO.Path]::Combine($env:APH_FIXTURE_ROOT, [string]$PID)
[IO.Directory]::CreateDirectory($root) | Out-Null
$utf8 = [Text.UTF8Encoding]::new($false)
function Record([string] $Name, [object] $Value) {
    $path = [IO.Path]::Combine($root, $Name)
    $temporary = $path + '.tmp'
    [IO.File]::WriteAllText($temporary, (ConvertTo-Json -InputObject $Value -Depth 24 -Compress), $utf8)
    [IO.File]::Move($temporary, $path)
}
function Send([object] $Frame) {
    [Console]::Out.WriteLine((ConvertTo-Json -InputObject $Frame -Depth 24 -Compress))
    [Console]::Out.Flush()
}
Record 'ready.json' ([ordered]@{ pid = $PID })
Send ([ordered]@{ version = 1; type = 'ready' })
$pending = @()
$watchCount = 0
$actionCount = 0
[FixtureConsole]::StartReader()
while ($true) {
    $line = [FixtureConsole]::Read()
    if ($null -ne $line) {
        $frame = ConvertFrom-Json -InputObject $line
        if ($frame.version -ne 1) { throw 'unsupported version' }
        switch ($frame.type) {
            'watch' {
                $watchCount++
                Record ('watch-' + $watchCount + '.json') ([ordered]@{
                    pid = $PID; request_id = $frame.request_id
                    subscription_id = $frame.subscription_id; watch_args = $frame.watch_args
                })
                if ($frame.watch_args.reject -eq $true) {
                    Send ([ordered]@{ version = 1; type = 'watch_result'; request_id = $frame.request_id; accepted = $false; error = 'fixture rejected watch' })
                } else { $pending += $frame }
            }
            'shutdown' {
                Record 'shutdown.json' ([ordered]@{ pid = $PID })
                exit 0
            }
            default { throw 'unsupported daemon frame' }
        }
    }
    elseif ([FixtureConsole]::Ended) { exit 0 }
    if ([IO.File]::Exists([IO.Path]::Combine($env:APH_FIXTURE_ROOT, 'ack'))) {
        foreach ($frame in $pending) {
            Send ([ordered]@{ version = 1; type = 'watch_result'; request_id = $frame.request_id; accepted = $true })
            if ($frame.watch_args.immediate -eq $true) {
                Send ([ordered]@{ version = 1; type = 'event'; subscription_id = $frame.subscription_id; context = "PowerShell probe`nquoted `"data`" C:\probe" })
            }
        }
        $pending = @()
    }
    $next = [IO.Path]::Combine($root, ('action-' + ($actionCount + 1) + '.json'))
    if ([IO.File]::Exists($next)) {
        $action = ConvertFrom-Json -InputObject ([IO.File]::ReadAllText($next, $utf8))
        foreach ($line in $action.lines) { [Console]::Out.WriteLine([string]$line) }
        [Console]::Out.Flush()
        $actionCount++
        Record ('action-done-' + $actionCount + '.json') ([ordered]@{ pid = $PID })
        if ($action.exit -eq $true) { exit 0 }
    }
    Start-Sleep -Milliseconds 5
}
