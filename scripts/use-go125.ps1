$ErrorActionPreference = "Stop"

$goRoot = "C:\Users\angelo\.g\go1.25\go"
if (-not (Test-Path -LiteralPath "$goRoot\bin\go.exe")) {
    throw "未找到星衍树项目所需的 Go 1.25 SDK：$goRoot"
}

$env:GOROOT = $goRoot
$env:Path = "$goRoot\bin;$env:Path"
$env:GOTOOLCHAIN = "local"

& "$goRoot\bin\go.exe" version
