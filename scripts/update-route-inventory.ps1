# Run from any directory. Reads source only; rewrites the migration inventory.
$ErrorActionPreference = 'Stop'
$backendRoot = Split-Path -Parent $PSScriptRoot
$sourceRoot = Join-Path (Split-Path -Parent $backendRoot) 'kirimku/src/app/api'
$implemented = [System.Collections.Generic.HashSet[string]]::new([System.StringComparer]::Ordinal)
foreach ($goFile in Get-ChildItem -LiteralPath (Join-Path $backendRoot 'internal/httpapi') -Filter '*.go') {
    if ($goFile.Name.EndsWith('_test.go')) { continue }
    $goSource = Get-Content -LiteralPath $goFile.FullName -Raw
    foreach ($route in [regex]::Matches($goSource, 'app\.(Get|Post|Put|Patch|Delete|Head|Options)\("([^"]+)"')) {
        $method = $route.Groups[1].Value.ToUpperInvariant()
        $routePath = $route.Groups[2].Value -replace ':([A-Za-z][A-Za-z0-9_]*)', '[$1]'
        [void] $implemented.Add("$method $routePath")
    }
}
$phases = @{
    'auth' = 2; 'profile' = 2; 'users' = 2; 'roles' = 2; 'employees' = 2
    'customers' = 3; 'warehouses' = 3; 'vehicles' = 3; 'routes' = 3
    'checkpoints' = 3; 'tariffs' = 3; 'settings' = 3; 'options' = 3; 'bank-accounts' = 3
    'shipments' = 4; 'shipment-details' = 4; 'public' = 4
    'pickups' = 5; 'deliveries' = 5; 'gudang' = 5
    'transports' = 6; 'return-tasks' = 6; 'capacity' = 6; 'fleet-capacity' = 6
    'payments' = 7; 'unpaid' = 7; 'invoices' = 7; 'invoice-lines' = 7
    'wallet' = 7; 'topups' = 7; 'withdrawals' = 7; 'commissions' = 7
    'partners' = 7; 'partner' = 7; 'repairs' = 7; 'finance' = 7
    'dashboard' = 8; 'audit-logs' = 8; 'mobile' = 8
}
$rows = foreach ($file in Get-ChildItem -LiteralPath $sourceRoot -Filter route.ts -Recurse) {
    $relative = [System.IO.Path]::GetRelativePath($sourceRoot, $file.FullName).Replace('\', '/')
    $path = '/api/' + ($relative -replace '/?route\.ts$', '')
    $path = $path.TrimEnd('/')
    $module = if ($path -eq '/api') { 'root' } else { $path.Split('/')[3] }
    $phase = if ($module -eq 'root') { 1 } else { $phases[$module] }
    if (-not $phase) { throw "Assign a phase for module: $module" }
    $content = Get-Content -LiteralPath $file.FullName -Raw
    $methods = [regex]::Matches($content, 'export\s+(?:async\s+)?function\s+(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)\s*\(|export\s+const\s+(GET|POST|PUT|PATCH|DELETE|HEAD|OPTIONS)\s*=')
    foreach ($match in $methods) {
        $method = if ($match.Groups[1].Success) { $match.Groups[1].Value } else { $match.Groups[2].Value }
        [pscustomobject]@{
            Phase = $phase
            Method = $method
            Path = $path
            Source = 'kirimku/src/app/api/' + $relative
            Status = if ($implemented.Contains("$method $path")) { 'implemented' } else { 'planned' }
        }
    }
}
$rows | Sort-Object Phase, Path, Method | Export-Csv -LiteralPath (Join-Path $backendRoot 'docs/route-inventory.csv') -NoTypeInformation -Encoding utf8
Write-Output "Inventoried $(@($rows).Count) method/path pairs across $(@($rows.Path | Sort-Object -Unique).Count) routes."
