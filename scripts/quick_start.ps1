# PowerShell 快速启动脚本 - 一键启动完整的演示环境

$ErrorActionPreference = "Stop"

Write-Host "🚀 Trusted Data Space Kernel - Quick Start" -ForegroundColor Green
Write-Host "==========================================" -ForegroundColor Green
Write-Host ""

# 检查依赖
function Check-Dependencies {
    Write-Host "Checking dependencies..." -ForegroundColor Cyan
    
    try {
        $null = Get-Command go -ErrorAction Stop
        Write-Host "✓ Go found" -ForegroundColor Green
    } catch {
        Write-Host "✗ Go is not installed" -ForegroundColor Red
        exit 1
    }
    
    try {
        $null = Get-Command protoc -ErrorAction Stop
        Write-Host "✓ protoc found" -ForegroundColor Green
    } catch {
        Write-Host "⚠ protoc not found, attempting to continue..." -ForegroundColor Yellow
    }
    
    try {
        $null = Get-Command openssl -ErrorAction Stop
        Write-Host "✓ OpenSSL found" -ForegroundColor Green
    } catch {
        Write-Host "✗ OpenSSL is not installed" -ForegroundColor Red
        Write-Host "  Download from: https://slproweb.com/products/Win32OpenSSL.html" -ForegroundColor Yellow
        exit 1
    }
    
    Write-Host ""
}

# 安装依赖
function Install-Dependencies {
    Write-Host "Installing Go dependencies..." -ForegroundColor Cyan
    go mod download
    Write-Host "✓ Dependencies installed" -ForegroundColor Green
    Write-Host ""
}

# 生成 Protobuf 代码
function Generate-Proto {
    if (Test-Path "api/v1") {
        Write-Host "Protobuf code already generated, skipping..." -ForegroundColor Yellow
    } else {
        Write-Host "Generating Protobuf code..." -ForegroundColor Cyan
        New-Item -ItemType Directory -Path "api/v1" -Force | Out-Null
        
        try {
            protoc --go_out=. --go_opt=paths=source_relative `
                --go-grpc_out=. --go-grpc_opt=paths=source_relative `
                proto/kernel/v1/*.proto
            Write-Host "✓ Protobuf code generated" -ForegroundColor Green
        } catch {
            Write-Host "⚠ Protobuf generation skipped (protoc not available)" -ForegroundColor Yellow
            Write-Host "  Please run 'make proto' after installing protoc" -ForegroundColor Yellow
        }
    }
    Write-Host ""
}

# 生成证书
function Generate-Certificates {
    if (Test-Path "certs/ca.crt") {
        Write-Host "Certificates already exist, skipping..." -ForegroundColor Yellow
    } else {
        Write-Host "Generating test certificates..." -ForegroundColor Cyan
        & .\scripts\gen_certs.ps1
    }
    Write-Host ""
}

# 创建目录
function Create-Directories {
    Write-Host "Creating directories..." -ForegroundColor Cyan
    New-Item -ItemType Directory -Path "logs" -Force | Out-Null
    New-Item -ItemType Directory -Path "bin" -Force | Out-Null
    Write-Host "✓ Directories created" -ForegroundColor Green
    Write-Host ""
}

# 编译
function Build-Project {
    Write-Host "Building kernel and connector..." -ForegroundColor Cyan
    go build -o bin/kernel.exe ./kernel/cmd
    go build -o bin/connector.exe ./connector/cmd
    Write-Host "✓ Build complete" -ForegroundColor Green
    Write-Host ""
}

# 启动演示
function Start-Demo {
    Write-Host "==========================================" -ForegroundColor Cyan
    Write-Host "Starting demonstration..." -ForegroundColor Cyan
    Write-Host "==========================================" -ForegroundColor Cyan
    Write-Host ""
    Write-Host "This will start:"
    Write-Host "  1. Kernel server"
    Write-Host "  2. Sender connector (connector-A)"
    Write-Host ""
    Write-Host "Press Ctrl+C to stop all services" -ForegroundColor Yellow
    Write-Host ""
    Start-Sleep -Seconds 2
    
    # 启动内核（后台）
    Write-Host "Starting kernel..." -ForegroundColor Cyan
    $kernelProcess = Start-Process -FilePath ".\bin\kernel.exe" -ArgumentList "-config", "config/kernel.yaml" -PassThru -RedirectStandardOutput "logs/kernel.log" -RedirectStandardError "logs/kernel_error.log" -NoNewWindow
    Write-Host "✓ Kernel started (PID: $($kernelProcess.Id))" -ForegroundColor Green
    Start-Sleep -Seconds 3
    
    # 启动发送方
    Write-Host ""
    Write-Host "================================================" -ForegroundColor Cyan
    Write-Host "Sender (Connector-A) - Starting data transfer" -ForegroundColor Cyan
    Write-Host "================================================" -ForegroundColor Cyan
    & .\bin\connector.exe -config config/connector.yaml -mode sender -receiver connector-B
    
    Write-Host ""
    Write-Host "==========================================" -ForegroundColor Green
    Write-Host "Demo completed!" -ForegroundColor Green
    Write-Host "==========================================" -ForegroundColor Green
    Write-Host ""
    Write-Host "Check logs:" -ForegroundColor Cyan
    Write-Host "  Kernel:  logs/kernel.log"
    Write-Host "  Audit:   logs/audit.log"
    Write-Host ""
    
    # 停止内核
    Write-Host "Stopping kernel..." -ForegroundColor Cyan
    Stop-Process -Id $kernelProcess.Id -Force -ErrorAction SilentlyContinue
    Start-Sleep -Seconds 1
    
    Write-Host "✓ All services stopped" -ForegroundColor Green
}

# 主流程
function Main {
    Check-Dependencies
    Install-Dependencies
    Generate-Proto
    Generate-Certificates
    Create-Directories
    Build-Project
    
    Write-Host ""
    Write-Host "==========================================" -ForegroundColor Green
    Write-Host "Setup complete! 🎉" -ForegroundColor Green
    Write-Host "==========================================" -ForegroundColor Green
    Write-Host ""
    Write-Host "You can now:" -ForegroundColor Cyan
    Write-Host ""
    Write-Host "1. Start kernel manually:"
    Write-Host "   .\bin\kernel.exe -config config/kernel.yaml"
    Write-Host ""
    Write-Host "2. Start connector as sender:"
    Write-Host "   .\bin\connector.exe -config config/connector.yaml -mode sender -receiver connector-B"
    Write-Host ""
    Write-Host "3. Start connector as receiver:"
    Write-Host "   .\bin\connector.exe -config config/connector-B.yaml -mode receiver -channel <channel-id>"
    Write-Host ""
    
    $response = Read-Host "Run automated demo now? (y/n)"
    if ($response -eq "y" -or $response -eq "Y") {
        Start-Demo
    }
}

# 运行
Main

