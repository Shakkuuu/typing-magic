#!/bin/bash

# WASMビルドスクリプト
# EbitengineクライアントをWASMとしてビルド

set -e

echo "Building WASM..."

cd client

# WASMとしてビルド
GOOS=js GOARCH=wasm go build -o ../static/typing-magic.wasm .

# wasm_exec.jsをコピー
# GOROOT=$(go env GOROOT)
# if [ -f "$GOROOT/misc/wasm/wasm_exec.js" ]; then
#     cp "$GOROOT/misc/wasm/wasm_exec.js" ../static/
#     echo "Copied wasm_exec.js to static/"
# else
#     echo "Warning: wasm_exec.js not found at $GOROOT/misc/wasm/wasm_exec.js"
#     echo "Please copy it manually from your Go installation"
# fi

echo "Build complete!"
echo "WASM file: static/typing-magic.wasm"
echo "Runtime: static/wasm_exec.js"

