# awgm-warpscout: build all targets with zig cc (zig 0.13).
# Produces out/awgm-warpscout-{mipsle,mips,arm64}, out/awgm-warpscout.exe and
# out/warpscout_test.exe; runs the Windows selftest + unit tests.
param(
    [string]$Zig = "",
    [switch]$NoTest
)
# native tools write warnings to stderr; rely on $LASTEXITCODE instead of "Stop"
$ErrorActionPreference = "Continue"
$here = Split-Path -Parent $MyInvocation.MyCommand.Path
$repo = Resolve-Path (Join-Path $here "..\..")
if (-not $Zig) { $Zig = Join-Path $repo ".cache\toolchains\zig\zig.exe" }
if (-not (Test-Path $Zig)) { throw "zig not found at $Zig" }

Push-Location $here
try {
    New-Item -ItemType Directory -Force -Path out | Out-Null
    $src = @("src/main.c", "src/wgcrypto.c", "src/wg.c", "src/netpkt.c", "src/json.c", "vendor/monocypher.c")
    $common = @("-std=c11", "-D_GNU_SOURCE", "-Os", "-Wall", "-Wextra", "-Wno-unused-parameter",
                "-ffunction-sections", "-fdata-sections", "-fno-asynchronous-unwind-tables")

    $targets = @(
        @{ name = "awgm-warpscout-mipsle"; triple = "mipsel-linux-musl"; extra = @("-msoft-float") },
        @{ name = "awgm-warpscout-mips";   triple = "mips-linux-musl";   extra = @("-msoft-float") },
        @{ name = "awgm-warpscout-arm64";  triple = "aarch64-linux-musl"; extra = @() }
    )
    foreach ($t in $targets) {
        Write-Host "==> $($t.name) ($($t.triple))"
        $args = @("cc", "-target", $t.triple) + $t.extra + $common + @("-static", "-s",
                  "-Wl,--gc-sections", "-o", "out/$($t.name)") + $src
        & $Zig @args
        if ($LASTEXITCODE -ne 0) { throw "build failed for $($t.name)" }
    }

    Write-Host "==> awgm-warpscout.exe (x86_64-windows-gnu)"
    $win = @("cc", "-target", "x86_64-windows-gnu") + $common + @("-s", "-Wl,--gc-sections",
             "-o", "out/awgm-warpscout.exe") + $src + @("-lws2_32", "-lbcrypt")
    & $Zig @win
    if ($LASTEXITCODE -ne 0) { throw "windows build failed" }

    Write-Host "==> warpscout_test.exe"
    $tst = @("cc", "-target", "x86_64-windows-gnu") + $common + @("-o", "out/warpscout_test.exe",
             "tests/test_main.c", "src/wgcrypto.c", "src/wg.c", "src/netpkt.c", "vendor/monocypher.c",
             "-lws2_32", "-lbcrypt")
    & $Zig @tst
    if ($LASTEXITCODE -ne 0) { throw "test build failed" }
    Remove-Item -ErrorAction SilentlyContinue out/*.pdb, out/*.lib

    if (-not $NoTest) {
        & ./out/awgm-warpscout.exe --selftest
        if ($LASTEXITCODE -ne 0) { throw "selftest failed" }
        & ./out/warpscout_test.exe
        if ($LASTEXITCODE -ne 0) { throw "unit tests failed" }
    }
    Get-ChildItem out | Select-Object Name, Length | Format-Table -AutoSize
} finally {
    Pop-Location
}
