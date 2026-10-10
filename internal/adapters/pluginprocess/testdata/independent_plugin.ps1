$ErrorActionPreference = 'Stop'
[Console]::InputEncoding = [System.Text.UTF8Encoding]::new($false)
[Console]::OutputEncoding = [System.Text.UTF8Encoding]::new($false)

function Send-Frame([object] $Frame) {
    $json = ConvertTo-Json -InputObject $Frame -Depth 16 -Compress
    [Console]::Out.WriteLine($json)
    [Console]::Out.Flush()
}

Send-Frame ([ordered]@{ version = 1; type = 'ready' })
while ($null -ne ($line = [Console]::In.ReadLine())) {
    try {
        $frame = ConvertFrom-Json -InputObject $line -ErrorAction Stop
    }
    catch {
        [Console]::Error.WriteLine('invalid frame')
        exit 2
    }

    if ($frame.version -ne 1) {
        [Console]::Error.WriteLine('unsupported version')
        exit 2
    }

    switch ($frame.type) {
        'watch' {
            Send-Frame ([ordered]@{
                version = 1
                type = 'watch_result'
                request_id = [string]$frame.request_id
                accepted = $true
            })
            Send-Frame ([ordered]@{
                version = 1
                type = 'event'
                subscription_id = [string]$frame.subscription_id
                context = 'independent PowerShell event'
            })
        }
        'shutdown' { exit 0 }
        default {
            [Console]::Error.WriteLine('unsupported frame type')
            exit 2
        }
    }
}

exit 0
